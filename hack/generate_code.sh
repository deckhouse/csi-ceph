#!/bin/bash

# run from repository root
go get k8s.io/code-generator/cmd/deepcopy-gen

cd /hooks/go/api
go run k8s.io/code-generator/cmd/deepcopy-gen -v 2 \
    --output-file zz_generated.deepcopy.go \
    --go-header-file ../../../hack/boilerplate.txt \
    ./v1alpha1

cd ../../..

cd /api
go run k8s.io/code-generator/cmd/deepcopy-gen -v 2 \
    --output-file zz_generated_by_hack.deepcopy.go \
    --go-header-file ../hack/boilerplate.txt \
    ./v1alpha1

cd ..
