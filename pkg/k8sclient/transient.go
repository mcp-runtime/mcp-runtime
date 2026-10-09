package k8sclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"net"
	"syscall"
	"time"
)

// IsTransient reports API or transport failures that can recover within a bounded wait.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsServiceUnavailable(err) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) || apierrors.IsTooManyRequests(err) || apierrors.IsInternalError(err) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var network net.Error
	return errors.As(err, &network) && network.Timeout()
}

// RetryTransient preserves permanent failures and caps recovery at timeout.
func RetryTransient(ctx context.Context, timeout, interval time.Duration, action func() error) error {
	var last error
	err := wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(context.Context) (bool, error) {
		last = action()
		if IsTransient(last) {
			return false, nil
		}
		return last == nil, last
	})
	if err != nil && last != nil && IsTransient(last) {
		return fmt.Errorf("operation did not recover before timeout: %w (last API error: %v)", err, last)
	}
	return err
}
