//go:build !darwin && !linux

package adapter

import "github.com/spf13/cobra"

func addServiceCommands(*cobra.Command) {}
