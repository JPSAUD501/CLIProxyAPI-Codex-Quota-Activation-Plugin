package quota

import _ "embed"

//go:embed status.html
var statusHTML []byte

//go:embed status.js
var statusJS []byte

func StatusHTML() []byte { return append([]byte(nil), statusHTML...) }
func StatusJS() []byte   { return append([]byte(nil), statusJS...) }
