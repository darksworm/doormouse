group "default" {
  targets = ["local"]
}

// The release workflow supplies version tags and OCI labels here.
target "docker-metadata-action" {}

target "image" {
  context    = "."
  dockerfile = "Dockerfile"
}

target "local" {
  inherits = ["image"]
  tags     = ["doormouse:local"]
  output   = ["type=docker"]
}

target "release" {
  inherits = ["image", "docker-metadata-action"]
  platforms = ["linux/amd64", "linux/arm64"]
}
