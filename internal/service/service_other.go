//go:build !darwin && !linux

package service

import "context"

func Install(context.Context, Options) (Status, error) {
	return Inspect(context.Background(), Options{}), ErrUnsupported
}

func Uninstall(context.Context, Options) (Status, error) {
	return Inspect(context.Background(), Options{}), ErrUnsupported
}

func Inspect(context.Context, Options) Status {
	return Status{Supported: false, Detail: ErrUnsupported.Error()}
}
