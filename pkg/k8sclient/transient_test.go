package k8sclient

import (
	"context"
	"errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"net"
	"syscall"
	"testing"
	"time"
)

func TestTransientRetryRecoversAfterAPIRefusal(t *testing.T) {
	attempts := 0
	err := RetryTransient(context.Background(), time.Second, time.Millisecond, func() error {
		attempts++
		if attempts == 1 {
			return &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("recovery err=%v attempts=%d", err, attempts)
	}
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "deployments"}, "operator", errors.New("denied"))
	attempts = 0
	err = RetryTransient(context.Background(), time.Second, time.Millisecond, func() error { attempts++; return forbidden })
	if !errors.Is(err, forbidden) || attempts != 1 {
		t.Fatalf("permanent error retried: %v attempts=%d", err, attempts)
	}
	err = RetryTransient(context.Background(), 5*time.Millisecond, time.Millisecond, func() error { return syscall.ECONNREFUSED })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unbounded or opaque timeout: %v", err)
	}
}
