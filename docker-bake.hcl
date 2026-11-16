// CI supplies the OCI artifact; local builds can link to the source target.
variable "DOCKTINEL_SOURCE" {
  default = "target:docktinel"
}
group "default" {
  targets = ["docktinel", "demo"]
}

target "docktinel" {
  context = "."
  tags    = ["docktinel:local"]
}

target "demo" {
  context = "examples/basic"
  tags    = ["docktinel-demo:local"]
  args = {
    DOCKTINEL_IMAGE = "docktinel-source"
  }
  contexts = {
    docktinel-source = DOCKTINEL_SOURCE
  }
}
