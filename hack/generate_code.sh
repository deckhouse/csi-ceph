#!/bin/bash

# run from repository root
cd api

go get k8s.io/code-generator/cmd/deepcopy-gen

go run k8s.io/code-generator/cmd/deepcopy-gen -v 2 \
    --output-file zz_generated_by_hack.deepcopy.go \
    --go-header-file ../hack/boilerplate.txt \
    ./v1alpha1

cd ..
