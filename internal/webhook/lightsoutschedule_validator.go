/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webhook

import (
	"context"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
)

var lightsoutschedulelog = logf.Log.WithName("lightsoutschedule-resource")

// LightsOutScheduleValidator handles validation with access to the API server
type LightsOutScheduleValidator struct {
	Client client.Client
}

// SetupWebhookWithManager sets up the webhook with the manager
func SetupWebhookWithManager(mgr ctrl.Manager) error {
	validator := &LightsOutScheduleValidator{
		Client: mgr.GetClient(),
	}
	defaulter := &LightsOutScheduleDefaulter{}
	return ctrl.NewWebhookManagedBy(mgr, &lightsoutv1alpha1.LightsOutSchedule{}).
		WithValidator(validator).
		WithDefaulter(defaulter).
		Complete()
}

// LightsOutScheduleDefaulter handles defaulting for LightsOutSchedule
type LightsOutScheduleDefaulter struct{}

// +kubebuilder:webhook:path=/mutate-lightsout-techsupport-mk-v1alpha1-lightsoutschedule,mutating=true,failurePolicy=fail,sideEffects=None,groups=lightsout.techsupport.mk,resources=lightsoutschedules,verbs=create;update,versions=v1alpha1,name=mlightsoutschedule.kb.io,admissionReviewVersions=v1

var _ admission.Defaulter[*lightsoutv1alpha1.LightsOutSchedule] = &LightsOutScheduleDefaulter{}

// Default implements admission.Defaulter
func (d *LightsOutScheduleDefaulter) Default(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutSchedule) error {
	lightsoutschedulelog.Info("defaulting", "name", schedule.Name)

	if schedule.Spec.Timezone == "" {
		schedule.Spec.Timezone = "UTC"
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-lightsout-techsupport-mk-v1alpha1-lightsoutschedule,mutating=false,failurePolicy=fail,sideEffects=None,groups=lightsout.techsupport.mk,resources=lightsoutschedules,verbs=create;update,versions=v1alpha1,name=vlightsoutschedule.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*lightsoutv1alpha1.LightsOutSchedule] = &LightsOutScheduleValidator{}

// ValidateCreate implements admission.Validator
func (v *LightsOutScheduleValidator) ValidateCreate(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutSchedule) (admission.Warnings, error) {
	lightsoutschedulelog.Info("validating create", "name", schedule.Name)

	if err := ValidateScheduleSpec(schedule); err != nil {
		return nil, err
	}

	// Check for overlapping schedules
	warnings := v.checkOverlappingSchedules(ctx, schedule, "")
	return warnings, nil
}

// ValidateUpdate implements admission.Validator
func (v *LightsOutScheduleValidator) ValidateUpdate(ctx context.Context, oldSchedule, schedule *lightsoutv1alpha1.LightsOutSchedule) (admission.Warnings, error) {
	lightsoutschedulelog.Info("validating update", "name", schedule.Name)

	if err := ValidateScheduleSpec(schedule); err != nil {
		return nil, err
	}

	// Check for overlapping schedules (exclude self)
	warnings := v.checkOverlappingSchedules(ctx, schedule, schedule.Name)
	return warnings, nil
}

// ValidateDelete implements admission.Validator
func (v *LightsOutScheduleValidator) ValidateDelete(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutSchedule) (admission.Warnings, error) {
	return nil, nil
}

// checkOverlappingSchedules checks if the schedule's namespace selection overlaps with other schedules
// and returns warnings if potential conflicts are detected.
// excludeName is used to exclude the current schedule during updates.
func (v *LightsOutScheduleValidator) checkOverlappingSchedules(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutSchedule, excludeName string) admission.Warnings {
	var warnings admission.Warnings

	// List all existing schedules
	var scheduleList lightsoutv1alpha1.LightsOutScheduleList
	if err := v.Client.List(ctx, &scheduleList); err != nil {
		lightsoutschedulelog.Error(err, "failed to list schedules for overlap check")
		// Don't fail validation if we can't check - just log and continue
		return nil
	}

	for _, existing := range scheduleList.Items {
		// Skip self (for updates)
		if existing.Name == excludeName {
			continue
		}

		// Check for overlap
		if SchedulesOverlap(schedule, &existing) {
			warning := fmt.Sprintf(
				"schedule %q may conflict with existing schedule %q - both target overlapping namespaces; "+
					"the managed-by annotation will prevent runtime conflicts, but workloads may be managed by either schedule",
				schedule.Name, existing.Name,
			)
			warnings = append(warnings, warning)
		}
	}

	return warnings
}

// SchedulesOverlap checks if two schedules have overlapping namespace targets
func SchedulesOverlap(a, b *lightsoutv1alpha1.LightsOutSchedule) bool {
	// Check explicit namespace overlap
	aNamespaces := make(map[string]bool)
	for _, ns := range a.Spec.Namespaces {
		aNamespaces[ns] = true
	}
	for _, ns := range b.Spec.Namespaces {
		if aNamespaces[ns] {
			return true
		}
	}

	// Check if both have namespace selectors (potential overlap)
	// This is a conservative check - if both use selectors, they might overlap
	if a.Spec.NamespaceSelector != nil && b.Spec.NamespaceSelector != nil {
		// If selectors are identical, they definitely overlap
		if LabelSelectorsEqual(a.Spec.NamespaceSelector, b.Spec.NamespaceSelector) {
			return true
		}
		// If one selector matches everything (empty), it overlaps with any other selector
		if IsEmptyLabelSelector(a.Spec.NamespaceSelector) || IsEmptyLabelSelector(b.Spec.NamespaceSelector) {
			return true
		}
		// Conservative: if both have non-empty selectors, warn about potential overlap
		// We can't evaluate selectors without listing namespaces, so we warn
		return true
	}

	// Check if one has explicit namespaces and the other has a catch-all selector
	if a.Spec.NamespaceSelector != nil && IsEmptyLabelSelector(a.Spec.NamespaceSelector) && len(b.Spec.Namespaces) > 0 {
		return true
	}
	if b.Spec.NamespaceSelector != nil && IsEmptyLabelSelector(b.Spec.NamespaceSelector) && len(a.Spec.Namespaces) > 0 {
		return true
	}

	return false
}

// LabelSelectorsEqual checks if two metav1.LabelSelector are identical
func LabelSelectorsEqual(a, b *metav1.LabelSelector) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	// Compare MatchLabels
	if len(a.MatchLabels) != len(b.MatchLabels) {
		return false
	}
	for k, v := range a.MatchLabels {
		if b.MatchLabels[k] != v {
			return false
		}
	}
	// Compare MatchExpressions (simplified - just check length and order)
	if len(a.MatchExpressions) != len(b.MatchExpressions) {
		return false
	}
	for i := range a.MatchExpressions {
		if a.MatchExpressions[i].Key != b.MatchExpressions[i].Key ||
			a.MatchExpressions[i].Operator != b.MatchExpressions[i].Operator {
			return false
		}
		if len(a.MatchExpressions[i].Values) != len(b.MatchExpressions[i].Values) {
			return false
		}
		for j := range a.MatchExpressions[i].Values {
			if a.MatchExpressions[i].Values[j] != b.MatchExpressions[i].Values[j] {
				return false
			}
		}
	}
	return true
}

// IsEmptyLabelSelector checks if a selector matches everything (empty selector)
func IsEmptyLabelSelector(selector *metav1.LabelSelector) bool {
	if selector == nil {
		return true
	}
	return len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0
}

// ValidateScheduleSpec validates the LightsOutSchedule spec
func ValidateScheduleSpec(schedule *lightsoutv1alpha1.LightsOutSchedule) error {
	var allErrs field.ErrorList

	// Validate upscale cron expression
	if err := validateCronExpression(schedule.Spec.Upscale); err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "upscale"),
			schedule.Spec.Upscale,
			fmt.Sprintf("invalid cron expression: %v", err),
		))
	}

	// Validate downscale cron expression
	if err := validateCronExpression(schedule.Spec.Downscale); err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "downscale"),
			schedule.Spec.Downscale,
			fmt.Sprintf("invalid cron expression: %v", err),
		))
	}

	// Validate timezone
	if schedule.Spec.Timezone != "" {
		if _, err := time.LoadLocation(schedule.Spec.Timezone); err != nil {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec", "timezone"),
				schedule.Spec.Timezone,
				"invalid IANA timezone",
			))
		}
	}

	// Validate namespace selection - at least one must be specified
	if schedule.Spec.NamespaceSelector == nil && len(schedule.Spec.Namespaces) == 0 {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec"),
			"at least one of namespaceSelector or namespaces must be specified",
		))
	}

	// Validate rate limit configs
	allErrs = append(allErrs, ValidateRateLimit(schedule.Spec.UpscaleRateLimit, "upscaleRateLimit")...)
	allErrs = append(allErrs, ValidateRateLimit(schedule.Spec.DownscaleRateLimit, "downscaleRateLimit")...)

	if len(allErrs) == 0 {
		return nil
	}
	return allErrs.ToAggregate()
}

func validateCronExpression(expr string) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	_, err := parser.Parse(expr)
	return err
}

// ValidateRateLimit validates a RateLimitConfig
func ValidateRateLimit(rateLimit *lightsoutv1alpha1.RateLimitConfig, fieldPath string) field.ErrorList {
	var errs field.ErrorList
	if rateLimit == nil {
		return errs
	}

	if rateLimit.BatchSize != nil && *rateLimit.BatchSize <= 0 {
		errs = append(errs, field.Invalid(
			field.NewPath("spec", fieldPath, "batchSize"),
			*rateLimit.BatchSize,
			"must be greater than 0",
		))
	}

	if rateLimit.DelayBetweenBatches != nil && rateLimit.DelayBetweenBatches.Duration < 0 {
		errs = append(errs, field.Invalid(
			field.NewPath("spec", fieldPath, "delayBetweenBatches"),
			rateLimit.DelayBetweenBatches.Duration,
			"must be non-negative",
		))
	}

	return errs
}
