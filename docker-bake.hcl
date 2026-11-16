// CI supplies the OCI artifact; local builds can link to the source target.
variable "DOCKTINEL_SOURCE" {
  default = "target:docktinel"
}
variable "GO_VERSION" {
  default = "1"
}

group "default" {
  targets = ["docktinel", "demo"]
}

target "docktinel" {
  context = "."
  tags    = ["docktinel:local"]
  args = {
    GO_VERSION = GO_VERSION
  }
}

target "demo" {
  context = "examples/basic"
  tags    = ["docktinel-demo:local"]
  args = {
    DOCKTINEL_IMAGE = "docktinel-source"
    GO_VERSION     = GO_VERSION
  }
  contexts = {
    docktinel-source = DOCKTINEL_SOURCE
  }
}
