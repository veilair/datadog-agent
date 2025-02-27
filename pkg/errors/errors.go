// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package errors

import "fmt"

type errorReason int

const (
	notFoundError errorReason = iota
	retriableError
	partialError
	unknownError
	disabledError
)

// AgentError is an error intended for consumption by a datadog pkg; it can also be
// reconstructed by clients from an error response. Public to allow easy type switches.
type AgentError struct {
	error
	message     string
	errorReason errorReason
}

// Error satisfies the error interface
func (e AgentError) Error() string {
	return e.message
}

// NewNotFound returns a new error which indicates that the object passed in parameter was not found.
func NewNotFound(notFoundObject string) *AgentError {
	return &AgentError{
		message:     fmt.Sprintf("%q not found", notFoundObject),
		errorReason: notFoundError,
	}
}

// IsNotFound returns true if the specified error was created by NewNotFound.
func IsNotFound(err error) bool {
	return reasonForError(err) == notFoundError
}

// NewRetriable returns a new error which indicates that the object passed in parameter couldn't be fetched and that the query can be retried.
func NewRetriable(retriableObj interface{}, err error) *AgentError {
	return &AgentError{
		message:     fmt.Sprintf("couldn't fetch %q: %v", retriableObj, err),
		errorReason: retriableError,
	}
}

// IsRetriable returns true if the specified error was created by NewRetriable.
func IsRetriable(err error) bool {
	return reasonForError(err) == retriableError
}

// NewPartial returns a new error which indicates that the object passed in parameter couldn't be fetched completely and that the query should be retried.
func NewPartial(partialObj interface{}) *AgentError {
	return &AgentError{
		message:     fmt.Sprintf("partially fetched %q, please retry", partialObj),
		errorReason: partialError,
	}
}

// IsPartial returns true if the specified error was created by NewPartial.
func IsPartial(err error) bool {
	return reasonForError(err) == partialError
}

// NewDisabled returns a new error which indicates that a particular Agent component is disabled.
func NewDisabled(component, reason string) *AgentError {
	return &AgentError{
		message:     fmt.Sprintf("component %s is disabled: %s", component, reason),
		errorReason: disabledError,
	}
}

// IsDisabled returns true if the specified error was created by NewDisabled.
func IsDisabled(err error) bool {
	return reasonForError(err) == disabledError
}

func reasonForError(err error) errorReason {
	switch t := err.(type) {
	case *AgentError:
		return t.errorReason
	}
	return unknownError
}
