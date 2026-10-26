// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: Copyright (c) 2024 Matthew Penner

//go:build unix

package ufs

import (
	"errors"

	"golang.org/x/sys/unix"
)

// convertErrorType converts errors into our custom errors to ensure consistent
// error values.
func convertErrorType(err error) error {
	if err == nil {
		return nil
	}

	var pErr *PathError
	if errors.As(err, &pErr) {
		if errno, ok := pErr.Err.(unix.Errno); ok {
			return errnoToPathError(errno, pErr.Op, pErr.Path)
		}
		return pErr
	}

	// If the error wasn't already a path error and is a errno, wrap it with
	// details that we can use to know there is something wrong with our
	// error wrapping somewhere.
	var errno unix.Errno
	if errors.As(err, &errno) {
		return &PathError{
			Op:   "!(UNKNOWN)",
			Path: "!(UNKNOWN)",
			Err:  err,
		}
	}

	return err
}

// ensurePathError ensures that err is a PathError. The op and path arguments
// are only used of the error isn't already a PathError.
func ensurePathError(err error, op, path string) error {
	if err == nil {
		return nil
	}

	// Check if the error is already a PathError.
	var pErr *PathError
	if errors.As(err, &pErr) {
		// If underlying error is a errno, convert it.
		//
		// DO NOT USE `errors.As` or whatever here, the error will either be
		// an errno, or it will be wrapped already.
		if errno, ok := pErr.Err.(unix.Errno); ok {
			return errnoToPathError(errno, pErr.Op, pErr.Path)
		}
		// Return the PathError as-is without modification.
		return pErr
	}

	// If the error is directly an errno, convert it to a PathError.
	var errno unix.Errno
	if errors.As(err, &errno) {
		return errnoToPathError(errno, op, path)
	}

	// Otherwise just wrap it as a PathError without any additional changes.
	return &PathError{
		Op:   op,
		Path: path,
		Err:  err,
	}
}

// errnoToPathError converts an errno into a proper path error.
func errnoToPathError(err unix.Errno, op, path string) error {
	switch err {
	// File exists
	case unix.EEXIST:
		return &PathError{
			Op:   op,
			Path: path,
			Err:  ErrExist,
		}
	// Is a directory
	case unix.EISDIR:
		return &PathError{
			Op:   op,
			Path: path,
			Err:  ErrIsDirectory,
		}
	// Not a directory
	case unix.ENOTDIR:
		return &PathError{
			Op:   op,
			Path: path,
			Err:  ErrNotDirectory,
		}
	// No such file or directory
	case unix.ENOENT:
		return &PathError{
			Op:   op,
			Path: path,
			Err:  ErrNotExist,
		}
	// Operation not permitted
	case unix.EPERM:
		return &PathError{
			Op:   op,
			Path: path,
			Err:  ErrPermission,
		}
	// Invalid cross-device link
	case unix.EXDEV:
		return &PathError{
			Op:   op,
			Path: path,
			Err:  ErrBadPathResolution,
		}
	// Too many levels of symbolic links
	case unix.ELOOP:
		return &PathError{
			Op:   op,
			Path: path,
			Err:  ErrBadPathResolution,
		}
	default:
		return &PathError{
			Op:   op,
			Path: path,
			Err:  err,
		}
	}
}
