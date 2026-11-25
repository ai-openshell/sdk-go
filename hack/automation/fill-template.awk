# SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# fill-template.awk: replace each {{KEY}} in a template with ENVIRON[KEY].
# Run it through fill-template.sh, which sets the environment and -v keys.
#
# One pass over each template line: a value is inserted as is and never
# scanned again, so upstream text such as "{{GH_TOKEN}}" inside a value
# stays literal. Only the keys in -v keys="K1 K2 ..." are read from ENVIRON;
# any other placeholder in the template stops the run.

BEGIN {
  n = split(keys, list, " ")
  for (i = 1; i <= n; i++) allowed[list[i]] = 1
}

{
  rest = $0
  out = ""
  while (match(rest, /\{\{[A-Z_]+\}\}/)) {
    key = substr(rest, RSTART + 2, RLENGTH - 4)
    if (!(key in allowed)) {
      printf "ERROR: unknown placeholder {{%s}} in %s\n", key, FILENAME > "/dev/stderr"
      exit 1
    }
    out = out substr(rest, 1, RSTART - 1) ENVIRON[key]
    rest = substr(rest, RSTART + RLENGTH)
  }
  print out rest
}
