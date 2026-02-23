---
title: "Authentication and Authorization"
type: "guide"
description: "Guide to the authentication and authorization system"
---

# Authentication and Authorization

This document covers the authentication and authorization system used in the project.

## OAuth Integration

Our application supports OAuth 2.0 for third-party authentication. The implementation is in `src/auth/oauth.go`.

### Token Refresh

The token refresh mechanism automatically renews expired access tokens using refresh tokens. This is handled by the `RefreshToken` function in `src/auth/oauth.go`.

Key configuration:
- Token expiry: 1 hour
- Refresh token expiry: 30 days
- Auto-refresh: enabled by default

### OAuth Providers

We support the following OAuth providers:
- Google (`src/auth/providers/google.go`)
- GitHub (`src/auth/providers/github.go`)

## Middleware

The authentication middleware (`src/middleware/auth.go`) validates tokens on every request:

1. Extract Bearer token from Authorization header
2. Validate token signature and expiry
3. Attach user context to request
4. Handle token refresh if token is near expiry

The token validation logic is in `src/middleware/token.go`.

## Session Management

Sessions are stored in Redis and managed by `src/auth/session.go`. Each session contains:
- User ID
- Access token
- Refresh token
- Expiry timestamp
- Device information
