//go:build !linux

package vdisk

import (
	"context"

	"emperror.dev/errors"
)

// ErrUnsupported reports that virtual disks need Linux loop devices, which
// this platform does not have.
var ErrUnsupported = errors.Sentinel("vdisk: virtual disks are only supported on Linux")

// ErrWouldNotFit exists so that callers can handle the shrink case uniformly
// across platforms; it is never returned here.
var ErrWouldNotFit = errors.Sentinel("vdisk: existing data does not fit within the requested size")

type manager struct{}

// New returns a Manager whose every operation reports that virtual disks are
// unavailable, so that Wings still builds and runs on non-Linux hosts.
func New() Manager { return manager{} }

func (manager) Supported() error { return ErrUnsupported }

func (manager) Ensure(context.Context, Options) error { return ErrUnsupported }

func (manager) Mounted(string) (bool, error) { return false, nil }

func (manager) Unmount(context.Context, string) error { return ErrUnsupported }

func (manager) Resize(context.Context, Options) error { return ErrUnsupported }

func (manager) Usage(string) (Metrics, error) { return Metrics{}, ErrUnsupported }

func (manager) Destroy(context.Context, Options) error { return ErrUnsupported }
