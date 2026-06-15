# templates/

Built-in service templates ship here and are compiled into the binary via
`go:embed` (ARCHITECTURE §6). Populated in **M1** (templating & generation).

Rules that will apply (DECISIONS D2, spec 02):

- Compose documents are built as a **typed model validated through
  `compose-go/v2`** — never string-templated YAML.
- Text templating (with **custom delimiters** so it never collides with shell
  `${VAR}` / Dockerfile `$TAG`) is reserved for unstructured artifacts:
  Dockerfiles, proxy/entrypoint configs, scripts.
- Rendering must be **deterministic**: identical inputs produce byte-identical
  output (sorted keys, fixed newlines). CI asserts this.
