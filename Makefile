
.PHONY: update-base-images-versions
update-base-images-versions:
	##~ Options: version=vMAJOR.MINOR.PATCH
	$(eval version ?= $(shell yq -r '.variables.BASE_IMAGES_VERSION' .gitlab-ci.yml))
	$(eval project ?= $(shell case "$(version)" in v0*) echo 'deckhouse%2Fbase-images';; *) echo 'deckhouse%2Fcontainer-base%2Fbase-images';; esac))
	curl --fail -sSLO https://github.com/api/v4/projects/$(project)/packages/generic/base_images/$(version)/base_images.yml
