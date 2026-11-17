/*
Copyright 2025 Flant JSC

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

package handlers

import (
	"fmt"
	"net/http"

	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/deckhouse/sds-common-lib/logger"
	"github.com/deckhouse/sds-common-lib/logger/kwhlogger"
)

const (
	SCValidatorID  = "SCValidator"
	CSCValidatorID = "CSCValidator"
)

func Handlers(l logger.Logger, cl kubernetes.Interface) (map[string]http.Handler, error) {
	SetLogger(l)
	kwhLog := kwhlogger.New(l)

	sc, err := GetValidatingWebhookHandler(SCValidate, SCValidatorID, &storagev1.StorageClass{}, kwhLog)
	if err != nil {
		return nil, fmt.Errorf("creating the StorageClass validator: %w", err)
	}
	csc, err := GetValidatingWebhookHandler(CSCValidate(cl), CSCValidatorID, nil, kwhLog)
	if err != nil {
		return nil, fmt.Errorf("creating the CephStorageClass validator: %w", err)
	}
	return map[string]http.Handler{
		"/sc-validate":  sc,
		"/csc-validate": csc,
	}, nil
}
