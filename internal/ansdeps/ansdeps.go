// Package ansdeps pins the ans-sdk-go packages named in CLAUDE.md as direct,
// compiled dependencies before any phase calls them. Delete this package once
// internal/verify and internal/mandate import the SDK directly.
package ansdeps

import (
	_ "github.com/agentnameservice/ans-sdk-go/ans"
	_ "github.com/agentnameservice/ans-sdk-go/pop"
	_ "github.com/agentnameservice/ans-sdk-go/verify"
	_ "github.com/agentnameservice/ans-sdk-go/verify/scitt"
)
