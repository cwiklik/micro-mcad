/*
Copyright 2023 IBM Corporation.

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

package controller

import (
	"errors"
	"time"

	mcadv1beta1 "github.com/cwiklik/micro-mcad/api/v1beta1"
)

// We cache AppWrapper phases because the reconciler cache does not immediately reflect updates.
// A Get or List call soon after an Update or Status.Update call may not reflect the latest object.
// See https://github.com/kubernetes-sigs/controller-runtime/issues/1622.
// Therefore we need to maintain our own cache to make sure new dispatching decisions accurately account
// for recent dispatching decisions. The cache is populated on phase updates.
// The cache is only meant to be used for AppWrapper List calls when computing available resources.
// We use the number of conditions to confirm our cached version is more recent than the reconciler cache.
// We remove cache entries when removing finalizers. TODO: We should purge the cache from stale entries
// periodically in case a finalizer is deleted  outside of our control.
// When reconciling an AppWrapper, we proactively detect and abort on conflicts as
// there is no point working on a stale AppWrapper. We know etcd updates will fail.
// This conflict detection reduces the probability of an etcd update failure but does not eliminate it.
// To defend against bugs in the cache implementation and egregious AppWrapper edits,
// we eventually give up on persistent conflicts and remove the AppWrapper phase from the cache.

// Cached AppWrapper
type CachedAppWrapper struct {
	// AppWrapper phase
	Phase mcadv1beta1.AppWrapperPhase

	// Number of conditions
	Conditions int

	// First conflict detected between reconciler cache and our cache if not nil
	Conflict *time.Time
}

// Add AppWrapper to cache
func (r *AppWrapperReconciler) addCachedPhase(appWrapper *mcadv1beta1.AppWrapper) {
	r.Cache[appWrapper.UID] = &CachedAppWrapper{Phase: appWrapper.Status.Phase, Conditions: len(appWrapper.Status.Conditions)}
}

// Remove AppWrapper from cache
func (r *AppWrapperReconciler) deleteCachedPhase(appWrapper *mcadv1beta1.AppWrapper) {
	delete(r.Cache, appWrapper.UID) // remove appWrapper from cache
}

// Get AppWrapper phase from cache if available
func (r *AppWrapperReconciler) getCachedPhase(appWrapper *mcadv1beta1.AppWrapper) mcadv1beta1.AppWrapperPhase {
	phase := appWrapper.Status.Phase
	if cached, ok := r.Cache[appWrapper.UID]; ok && cached.Conditions > len(appWrapper.Status.Conditions) {
		phase = cached.Phase // use our cached phase if more current than reconciler cache
	}
	return phase
}

// Check whether reconciler cache and our cache appear to be in sync
func (r *AppWrapperReconciler) checkCachedPhase(appWrapper *mcadv1beta1.AppWrapper) error {
	if cached, ok := r.Cache[appWrapper.UID]; ok {
		// check number of conditions
		if cached.Conditions > len(appWrapper.Status.Conditions) {
			// reconciler cache appears to be behind
			if cached.Conflict != nil {
				if time.Now().After(cached.Conflict.Add(cacheConflictTimeout)) {
					// this has been going on for a while, assume something is wrong with our cache
					delete(r.Cache, appWrapper.UID)
					return errors.New("persistent cache conflict") // force redo
				}
			} else {
				now := time.Now()
				cached.Conflict = &now // remember when conflict started
			}
			return errors.New("stale reconciler cache") // force redo
		}
		if cached.Conditions < len(appWrapper.Status.Conditions) || cached.Phase != appWrapper.Status.Phase {
			// something is wrong with our cache
			delete(r.Cache, appWrapper.UID)
			return errors.New("stale phase cache") // force redo
		}
		// caches appear to be in sync
		cached.Conflict = nil // clear conflict timestamp
	}
	return nil
}
