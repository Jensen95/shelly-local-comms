//go:build tools

// Package tools pins dependencies that implementation packages are about
// to use, so go.mod stays stable while features land in parallel. Remove
// imports from here as real code picks them up.
package tools

import (
	_ "github.com/charmbracelet/bubbles/table"
	_ "github.com/charmbracelet/bubbletea"
	_ "github.com/charmbracelet/lipgloss"
	_ "github.com/gorilla/websocket"
	_ "github.com/grandcat/zeroconf"
)
