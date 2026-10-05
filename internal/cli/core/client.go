package core

// This file implements KubectlClient, a wrapper around kubectl command execution.
// It provides validation, security checks, and a clean interface for running kubectl commands.

import (
	"fmt"
	"io"
	"os"
)

// KubectlClient wraps kubectl command execution with validation.
type KubectlClient struct {
	exec       Executor
	validators []ExecValidator
}

// NewKubectlClient creates a KubectlClient with default validators.
func NewKubectlClient(exec Executor) (*KubectlClient, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, wrapWithBase(ErrGetWorkingDirectoryFailed, err, fmt.Sprintf("get working directory: %v", err))
	}
	return &KubectlClient{
		exec: exec,
		validators: []ExecValidator{
			NoControlChars(), // Prevent YAML/command injection via control chars
			PathUnder(root),
		},
	}, nil
}

// CommandArgs builds a kubectl command with the given arguments.
// Validates arguments against configured validators before building.
func (c *KubectlClient) CommandArgs(args []string) (Command, error) {
	return c.exec.Command("kubectl", args, c.validators...)
}

// Output runs kubectl with the given arguments and returns stdout.
func (c *KubectlClient) Output(args []string) ([]byte, error) {
	cmd, err := c.CommandArgs(args)
	if err != nil {
		return nil, err
	}
	return cmd.Output()
}

// CombinedOutput runs kubectl with the given arguments and returns combined stdout/stderr.
func (c *KubectlClient) CombinedOutput(args []string) ([]byte, error) {
	cmd, err := c.CommandArgs(args)
	if err != nil {
		return nil, err
	}
	return cmd.CombinedOutput()
}

// Run runs kubectl with the given arguments.
func (c *KubectlClient) Run(args []string) error {
	cmd, err := c.CommandArgs(args)
	if err != nil {
		return err
	}
	return cmd.Run()
}

// RunWithOutput runs kubectl with the given arguments, piping to the provided writers.
func (c *KubectlClient) RunWithOutput(args []string, stdout, stderr io.Writer) error {
	cmd, err := c.CommandArgs(args)
	if err != nil {
		return err
	}
	cmd.SetStdout(stdout)
	cmd.SetStderr(stderr)
	return cmd.Run()
}

var kubectlClient = mustNewKubectlClient()

// DefaultKubectlRunner returns the shared kubectl runner used by CLI commands.
func DefaultKubectlRunner() KubectlRunner {
	return kubectlClient
}

// DefaultKubectlClient returns the shared kubectl client used by CLI commands.
func DefaultKubectlClient() *KubectlClient {
	return kubectlClient
}

// DefaultExecutor returns the shared process executor used by CLI commands.
func DefaultExecutor() Executor {
	return execExecutor
}

func mustNewKubectlClient() *KubectlClient {
	client, err := NewKubectlClient(execExecutor)
	if err != nil {
		panic(err)
	}
	return client
}

// SwapDefaultKubectlClient replaces the shared kubectl client (tests only).
func SwapDefaultKubectlClient(c *KubectlClient) (restore func()) {
	prev := kubectlClient
	kubectlClient = c
	return func() { kubectlClient = prev }
}
