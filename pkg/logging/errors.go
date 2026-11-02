/*
Copyright 2026 The kcp Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package logging

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"syscall"

	"k8s.io/klog/v2"
)

// IsClientDisconnect reports whether err means the client went away rather than
// the server or a backend failing. ctx is the inbound request's context.
//
// A proxy sees this routinely and it is not a fault: a client that cancels a
// watch, closes a kubectl session or simply goes offline cancels the inbound
// request, which cancels the outbound one. Logging that at error level turns
// ordinary client behaviour into a stream of errors that hides real problems, so
// callers log these at a high verbosity instead, and skip writing a response
// that nobody is left to read.
func IsClientDisconnect(ctx context.Context, err error) bool {
	// The inbound request being cancelled is the most direct signal: whatever the
	// outbound error says, there is no client left to serve.
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return true
	}
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, http.ErrAbortHandler) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET)
}

// streamEndings are the error texts httputil.ReverseProxy reports when a
// response stream ends because a deadline or the client ended it, rather than
// because the backend failed.
var streamEndings = []string{
	context.Canceled.Error(),
	context.DeadlineExceeded.Error(),
	syscall.EPIPE.Error(),
	syscall.ECONNRESET.Error(),
}

// NewReverseProxyErrorLog returns a logger for httputil.ReverseProxy.ErrorLog.
//
// Without one, the proxy writes to the standard library logger, which ends up in
// klog without a level a log pipeline can trust. Most of what it reports there
// is a watch reaching its timeout mid-stream ("read error during body copy:
// context deadline exceeded") or a client going away, which happens all the
// time on a busy proxy and is not a fault. Those are logged at V(4); anything
// else is logged as an error.
func NewReverseProxyErrorLog(logger klog.Logger) *log.Logger {
	return log.New(reverseProxyLogWriter{logger: logger}, "", 0)
}

type reverseProxyLogWriter struct {
	logger klog.Logger
}

func (w reverseProxyLogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	for _, ending := range streamEndings {
		if strings.Contains(msg, ending) {
			w.logger.V(4).Info(msg)
			return len(p), nil
		}
	}
	w.logger.Error(nil, msg)
	return len(p), nil
}
