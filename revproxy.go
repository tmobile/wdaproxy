package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	logr "github.com/sirupsen/logrus"
)

type transport struct {
	http.RoundTripper
}

func drainBody(src io.ReadCloser) (string, io.ReadCloser, error) {
  var err error
	if src == nil || src == http.NoBody {
		// No copying needed. Preserve the magic sentinel meaning of NoBody.
		return "empty", http.NoBody, nil
	}
	var buf bytes.Buffer
	if _, err = buf.ReadFrom(src); err != nil {
		return "err", src, err
	}
	if err = src.Close(); err != nil {
		return "err", src, err
	}
	return buf.String(), ioutil.NopCloser(bytes.NewReader(buf.Bytes())), nil
}

func (t *transport) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	// rewrite url
	if strings.HasPrefix(req.RequestURI, "/origin/") {
		req.URL.Path = req.RequestURI[len("/origin"):]
		return t.RoundTripper.RoundTrip(req)
	}

	// request
	resp, err = t.RoundTripper.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// rewrite body
	if req.URL.Path == "/status" {
		jsonResp := &statusResp{}
		err = json.NewDecoder(resp.Body).Decode(jsonResp)
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
		jsonResp.Value["device"] = map[string]interface{}{
			"udid": udid,
			"name": udidNames[udid],
		}
		data, _ := json.Marshal(jsonResp)
		// update body and fix length
		resp.Body = ioutil.NopCloser(bytes.NewReader(data))
		resp.ContentLength = int64(len(data))
		resp.Header.Set("Content-Length", strconv.Itoa(len(data)))
		return resp, nil
	}
	
	uri := req.RequestURI
	
	var save string = ""
	if uri != "/screenshot" {
	  save, resp.Body, _ = drainBody(resp.Body)
	}
	
	if uri != "/status" {
   logr.WithFields( logr.Fields{
      "type": "req.done",
      "uri": req.RequestURI,
      "body_out": save,
    } ).Info("req done")
  }
	  
	return resp, nil
}

type JSONLog struct {
	  file      *os.File
	  fileName  string
	  formatter *logr.JSONFormatter
	  failed    bool
}
func ( hook *JSONLog ) Fire( entry *logr.Entry ) error {
    // If we have failed to write to the file; don't bother trying
    if hook.failed { return nil }
    jsonformat, _ := hook.formatter.Format( entry )
    _, err := hook.file.WriteString( string( jsonformat ) )
    if err != nil {
        hook.failed = true
        fmt.Fprintf( os.Stderr, "Cannot write to logfile: %v", err )
        return err
    }
    return nil
}
func (hook *JSONLog) Levels() []logr.Level {
    return []logr.Level{ logr.PanicLevel, logr.FatalLevel, logr.ErrorLevel, logr.WarnLevel, logr.InfoLevel, logr.DebugLevel }
}

func AddJSONLog( fileName string ) {
    logFile, err := os.OpenFile( fileName, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0666 )
    if err != nil {
        fmt.Fprintf( os.Stderr, "Unable to open file for writing: %v", err )
    }
    
    fileHook := JSONLog{
        file: logFile,
        fileName: fileName,
        formatter: &logr.JSONFormatter{},
        failed: false,
    }
    
    logr.AddHook( &fileHook )
}

func setup_log( debug bool ) {
    logFile := "req_log.json"
    
    if debug {
        logr.WithFields( logr.Fields{ "type": "debug_status" } ).Warn("Debugging enabled")
        logr.SetLevel( logr.DebugLevel )
    } else {
        logr.SetLevel( logr.InfoLevel )
    }
    
    AddJSONLog( logFile )
}

/*func (c *loggingWriter) Header() http.Header {
    return c.ResponseWriter.Header()
}

func (c *loggingWriter) Write(data []byte) (int, error) {
    fmt.Println(string(data)) //get response here
    return c.ResponseWriter.Write(data)
}

func (c *loggingWriter) WriteHeader(i int) {
    c.ResponseWriter.WriteHeader(i)
}

type loggingWriter struct {
    http.ResponseWriter
}

func NewLoggingWriter(w http.ResponseWriter) *loggingWriter {
    return &loggingWriter{w}
}*/

func NewReverseProxyHandlerFunc(targetURL *url.URL) http.HandlerFunc {
	httpProxy := httputil.NewSingleHostReverseProxy(targetURL)
	httpProxy.Transport = &transport{http.DefaultTransport}
	setup_log( false )
	return func(rw http.ResponseWriter, r *http.Request) {
	  uri := r.RequestURI
	  if uri != "/status" {
	    var save string
	    save, r.Body, _ = drainBody(r.Body)
	    
      logr.WithFields( logr.Fields{
        "type": "req.start",
        "uri": r.RequestURI,
        "body_in": save,
      } ).Info("req start")
    }
	  
		httpProxy.ServeHTTP( rw, r)
	}
}

type fakeProxy struct {
}

func (p *fakeProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Println("FAKE", r.RequestURI)
	log.Println("P", r.URL.Path)
	io.WriteString(w, "Fake")
}

func NewAppiumProxyHandlerFunc(targetURL *url.URL) http.HandlerFunc {
	httpProxy := httputil.NewSingleHostReverseProxy(targetURL)
	rt := mux.NewRouter()
	rt.HandleFunc("/wd/hub/sessions", func(w http.ResponseWriter, r *http.Request) {
		data, _ := json.MarshalIndent(map[string]interface{}{
			"status":    0,
			"value":     []string{},
			"sessionId": nil,
		}, "", "    ")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Write(data)
	})
	rt.HandleFunc("/wd/hub/session/{sessionId}/window/current/size", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.Replace(r.URL.Path, "/current/size", "/size", -1)
		r.URL.Path = r.URL.Path[len("/wd/hub"):]
		httpProxy.ServeHTTP(w, r)
	})
	rt.Handle("/wd/hub/{subpath:.*}", http.StripPrefix("/wd/hub", httpProxy))
	return rt.ServeHTTP
}
