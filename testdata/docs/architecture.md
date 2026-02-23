---
title: "System Architecture"
type: "architecture"
description: "Overview of the system architecture and design decisions"
---

# System Architecture

This document describes the overall system architecture.

## Overview

The system follows a microservices architecture with the following components:

- **API Gateway** (`src/gateway/main.go`): Routes requests to services
- **Auth Service** (`src/auth/service.go`): Handles authentication
- **User Service** (`src/users/service.go`): Manages user data
- **Notification Service** (`src/notifications/service.go`): Sends notifications

## Data Flow

1. Client sends request to API Gateway
2. Gateway validates auth token via Auth Service
3. Gateway routes to appropriate service
4. Service processes request and returns response

## Database

We use PostgreSQL for persistent storage with the schema defined in `db/migrations/`. The database client is in `src/db/client.go`.

## Configuration

All services read configuration from `config/config.yaml` with environment-specific overrides.
