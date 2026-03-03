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

	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	lightsoutv1alpha1 "github.com/gjorgji-ts/lightsout/api/v1alpha1"
)

var lightsoutnamespaceschedulelog = logf.Log.WithName("lightsoutnamespaceschedule-resource")

// LightsOutNamespaceScheduleValidator handles validation
type LightsOutNamespaceScheduleValidator struct {
	Client client.Client
}

// LightsOutNamespaceScheduleDefaulter handles defaulting
type LightsOutNamespaceScheduleDefaulter struct{}

// +kubebuilder:webhook:path=/mutate-lightsout-techsupport-mk-v1alpha1-lightsoutnamespaceschedule,mutating=true,failurePolicy=fail,sideEffects=None,groups=lightsout.techsupport.mk,resources=lightsoutnamespaceschedules,verbs=create;update,versions=v1alpha1,name=mlightsoutnamespaceschedule.kb.io,admissionReviewVersions=v1

var _ admission.Defaulter[*lightsoutv1alpha1.LightsOutNamespaceSchedule] = &LightsOutNamespaceScheduleDefaulter{}

// Default implements admission.Defaulter
func (d *LightsOutNamespaceScheduleDefaulter) Default(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule) error {
	lightsoutnamespaceschedulelog.Info("defaulting", "name", schedule.Name, "namespace", schedule.Namespace)
	if schedule.Spec.Timezone == "" {
		schedule.Spec.Timezone = "UTC"
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-lightsout-techsupport-mk-v1alpha1-lightsoutnamespaceschedule,mutating=false,failurePolicy=fail,sideEffects=None,groups=lightsout.techsupport.mk,resources=lightsoutnamespaceschedules,verbs=create;update,versions=v1alpha1,name=vlightsoutnamespaceschedule.kb.io,admissionReviewVersions=v1

var _ admission.Validator[*lightsoutv1alpha1.LightsOutNamespaceSchedule] = &LightsOutNamespaceScheduleValidator{}

// ValidateCreate implements admission.Validator
func (v *LightsOutNamespaceScheduleValidator) ValidateCreate(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule) (admission.Warnings, error) {
	lightsoutnamespaceschedulelog.Info("validating create", "name", schedule.Name, "namespace", schedule.Namespace)
	if err := ValidateScheduleCore(&schedule.Spec.LightsOutScheduleCore); err != nil {
		return nil, err
	}
	warnings := v.checkGlobalScheduleOverlap(ctx, schedule)
	return warnings, nil
}

// ValidateUpdate implements admission.Validator
func (v *LightsOutNamespaceScheduleValidator) ValidateUpdate(ctx context.Context, oldSchedule, schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule) (admission.Warnings, error) {
	lightsoutnamespaceschedulelog.Info("validating update", "name", schedule.Name, "namespace", schedule.Namespace)
	if err := ValidateScheduleCore(&schedule.Spec.LightsOutScheduleCore); err != nil {
		return nil, err
	}
	warnings := v.checkGlobalScheduleOverlap(ctx, schedule)
	return warnings, nil
}

// ValidateDelete implements admission.Validator
func (v *LightsOutNamespaceScheduleValidator) ValidateDelete(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule) (admission.Warnings, error) {
	return nil, nil
}

// checkGlobalScheduleOverlap warns if any LightsOutSchedule already claims this namespace.
func (v *LightsOutNamespaceScheduleValidator) checkGlobalScheduleOverlap(ctx context.Context, schedule *lightsoutv1alpha1.LightsOutNamespaceSchedule) admission.Warnings {
	var warnings admission.Warnings

	var globalList lightsoutv1alpha1.LightsOutScheduleList
	if err := v.Client.List(ctx, &globalList); err != nil {
		lightsoutnamespaceschedulelog.Error(err, "failed to list global schedules for overlap check")
		return admission.Warnings{"overlap check could not be completed: failed to list LightsOutSchedule resources; verify manually that no global schedule targets this namespace"}
	}

	for _, global := range globalList.Items {
		// Check if the global schedule explicitly lists this namespace
		for _, ns := range global.Spec.Namespaces {
			if ns == schedule.Namespace {
				warnings = append(warnings, fmt.Sprintf(
					"LightsOutSchedule %q targets namespace %q; the namespace schedule will take precedence, "+
						"but you should verify the interaction is intended",
					global.Name, schedule.Namespace,
				))
				break
			}
		}

		// Also warn for selector-based global schedules — we can't evaluate the selector
		// at admission time, but the user should know the interaction will be resolved at runtime.
		if global.Spec.NamespaceSelector != nil {
			warnings = append(warnings, fmt.Sprintf(
				"LightsOutSchedule %q uses a namespaceSelector that may include namespace %q; "+
					"the namespace schedule will take precedence at runtime",
				global.Name, schedule.Namespace,
			))
		}
	}

	return warnings
}
