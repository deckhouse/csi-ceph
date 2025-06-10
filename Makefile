
.PHONY: update-base-images-versions
update-base-images-versions:
	##~ Options: version=vMAJOR.MINOR.PATCH
	$(eval version ?= $(shell yq -r '.env.BASE_IMAGES_VERSION' .github/workflows/build_dev.yml))
	curl --fail -sSLO https://fox.flant.com/api/v4/projects/deckhouse%2Fbase-images/packages/generic/base_images/$(version)/base_images.yml
