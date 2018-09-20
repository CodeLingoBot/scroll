package scroll

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/mailgun/log"
	"github.com/mailgun/scroll/vulcand"
)

// When Handler or HandlerWithBody is used, this function will be called after every request with a log message.
// If nil, defaults to github.com/mailgun/log.Infof.
var LogRequest func(*http.Request, int, time.Duration, error)

// Response objects that apps' handlers are advised to return.
//
// Allows to easily return JSON-marshallable responses, e.g.:
//
//  Response{"message": "OK"}
type Response map[string]interface{}

// Represents handler's specification.
type Spec struct {
	// List of HTTP methods the handler should match.
	Methods []string

	// List of paths the handler should match. A separate handler will be registered for each one of them.
	Paths []string

	// Key/value pairs of specific HTTP headers the handler should match (e.g. Content-Type).
	Headers []string

	// A handler function to use. Just one of these should be provided.
	RawHandler      http.HandlerFunc
	Handler         HandlerFunc
	HandlerWithBody HandlerWithBodyFunc

	// Unique identifier used when emitting performance metrics for the handler.
	MetricName string

	// Controls the handler's accessibility via vulcan (public or protected). If not specified, public is assumed.
	Scope Scope

	// Vulcan middlewares to register with the handler. When registering, middlewares are assigned priorities
	// according to their positions in the list: a middleware that appears in the list earlier is executed first.
	Middlewares []vulcand.Middleware

	// When Handler or HandlerWithBody is used, this function will be called after every request with a log message.
	// If nil, defaults to github.com/mailgun/log.Infof.
	LogRequest func(r *http.Request, status int, elapsedTime time.Duration, err error)
}

// Given a map of parameters url decode each parameter
func DecodeParams(src map[string]string) map[string]string {
	results := make(map[string]string, len(src))
	for key, param := range src {
		encoded, err := url.QueryUnescape(param)
		if err != nil {
			encoded = param
		}
		results[key] = encoded
	}
	return results
}

// Defines the signature of a handler function that can be registered by an app.
//
// The 3rd parameter is a map of variables extracted from the request path, e.g. if a request path was:
//  /resources/{resourceID}
// and the request was made to:
//  /resources/1
// then the map will contain the resource ID value:
//  {"resourceID": 1}
//
// A handler function should return a JSON marshallable object, e.g. Response.
type HandlerFunc func(http.ResponseWriter, *http.Request, map[string]string) (interface{}, error)

// Wraps the provided handler function encapsulating boilerplate code so handlers do not have to
// implement it themselves: parsing a request's form, formatting a proper JSON response, emitting
// the request stats, etc.
func MakeHandler(app *App, fn HandlerFunc, spec Spec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var response interface{}
		var status int
		var err error

		start := time.Now()
		if err = parseForm(r); err != nil {
			err = fmt.Errorf("Failed to parse request form: %v", err)
			response = Response{"message": err.Error()}
			status = http.StatusInternalServerError
		} else {
			response, err = fn(w, r, DecodeParams(mux.Vars(r)))
			if err != nil {
				response, status = responseAndStatusFor(err)
			} else {
				status = http.StatusOK
			}
		}
		elapsedTime := time.Since(start)
		LogRequest(r, status, elapsedTime, err)
		app.stats.TrackRequest(spec.MetricName, status, elapsedTime)

		Reply(w, response, status)
	}
}

// Defines a signature of a handler function, just like HandlerFunc.
//
// In addition to the HandlerFunc a request's body is passed into this function as a 4th parameter.
type HandlerWithBodyFunc func(http.ResponseWriter, *http.Request, map[string]string, []byte) (interface{}, error)

// Make a handler out of HandlerWithBodyFunc, just like regular MakeHandler function.
func MakeHandlerWithBody(app *App, fn HandlerWithBodyFunc, spec Spec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var response interface{}
		var body []byte
		var status int
		var err error

		start := time.Now()
		if err = parseForm(r); err != nil {
			err = fmt.Errorf("Failed to parse request form: %v", err)
			response = Response{"message": err.Error()}
			status = http.StatusInternalServerError
			goto end
		}

		body, err = ioutil.ReadAll(r.Body)
		if err != nil {
			err = fmt.Errorf("Failed to read request body: %v", err)
			response = Response{"message": err.Error()}
			status = http.StatusInternalServerError
			goto end
		}

		response, err = fn(w, r, mux.Vars(r), body)
		if err != nil {
			response, status = responseAndStatusFor(err)
		} else {
			status = http.StatusOK
		}

	end:
		elapsedTime := time.Since(start)
		LogRequest(r, status, elapsedTime, err)
		app.stats.TrackRequest(spec.MetricName, status, elapsedTime)

		Reply(w, response, status)
	}
}

// jsonMarshall marshal without escaping "<", ">" and "&"
//
// By default golang standard library escapes "<", ">" and "&"
// to keep some browsers from misinterpreting JSON output as HTML.
// If API returns a link with "&" it breaks the link.
// This can't be fixed by a custom marshaler.
//
// For more details see:
// https://stackoverflow.com/questions/28595664/how-to-stop-json-marshal-from-escaping-and
func jsonMarshal(t interface{}) ([]byte, error) {
	buffer := &bytes.Buffer{}
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(t)
	return buffer.Bytes(), err
}

// Reply with the provided HTTP response and status code.
//
// Response body must be JSON-marshallable, otherwise the response
// will be "Internal Server Error".
func Reply(w http.ResponseWriter, response interface{}, status int) {
	// marshal the body of the response
	marshalledResponse, err := jsonMarshal(response)
	if err != nil {
		marshalledResponse = []byte(fmt.Sprintf(`{"message": "Failed to marshal response: %v %v"}`, response, err))
		status = http.StatusInternalServerError
		LogRequest(nil, status, time.Nanosecond, err)
	}

	// write JSON response
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(marshalledResponse)
}

// ReplyError converts registered error into HTTP response code and writes it back.
func ReplyError(w http.ResponseWriter, err error) {
	response, status := responseAndStatusFor(err)
	Reply(w, response, status)
}

// ReplyInternalError logs the error message and replies with a 500 status code.
func ReplyInternalError(w http.ResponseWriter, message string) {
	LogRequest(nil, 500, time.Nanosecond, errors.New(message))
	Reply(w, Response{"message": message}, http.StatusInternalServerError)
}

// GetVarSafe is a helper function that returns the requested variable from URI with allowSet
// providing input sanitization. If an error occurs, returns either a `MissingFieldError`
// or an `UnsafeFieldError`.
func GetVarSafe(r *http.Request, variableName string, allowSet AllowSet) (string, error) {
	vars := mux.Vars(r)
	variableValue, ok := vars[variableName]

	if !ok {
		return "", MissingFieldError{variableName}
	}

	err := allowSet.IsSafe(variableValue)
	if err != nil {
		return "", UnsafeFieldError{variableName, err.Error()}
	}

	return variableValue, nil
}

// Parse the request data based on its content type.
func parseForm(r *http.Request) error {
	if isMultipart(r) == true {
		return r.ParseMultipartForm(0)
	} else {
		return r.ParseForm()
	}
}

//Log request
func logRequest(r *http.Request, status int, elapsedTime time.Duration, err error) {
	log.Infof("Request(Status=%v, Method=%v, Path=%v, Form=%v, Time=%v, Error=%v)",
		status, r.Method, r.URL, r.Form, elapsedTime, err)
}

// Determine whether the request is multipart/form-data or not.
func isMultipart(r *http.Request) bool {
	contentType := r.Header.Get("Content-Type")
	return strings.HasPrefix(contentType, "multipart/form-data")
}
