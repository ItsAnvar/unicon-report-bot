package migrations

import _ "embed"

//go:embed 004_delivery_queue.sql
var DeliverySchema string

//go:embed 005_onboarding_combined_department.sql
var OnboardingSchema string

//go:embed 006_roles_and_management.sql
var ManagementSchema string

//go:embed 007_eri_korm_department.sql
var EriKormSchema string
