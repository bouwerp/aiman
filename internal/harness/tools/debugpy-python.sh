#!/bin/sh
# debugpy-python: an interpreter under debugpy, for a variable named in
# app.debug.wrap_env. It runs $AIMAN_DEBUGPY_PYTHON (python3 unless the
# harness file names a runtime) under debugpy on the run's debug address,
# waiting for a client before the program runs. One process at a time holds
# the debug address: the first one records its pid in /tmp/aiman-debugpy,
# and an interpreter started while that process lives runs without a server
# of its own. A subprocess that a debugged interpreter starts through this
# wrapper still joins the debugger: debugpy adds its own arguments to the
# command line, and the wrapper passes them on.
py=${AIMAN_DEBUGPY_PYTHON:-python3}
PYTHONPATH=/aiman-tools/debugpy${PYTHONPATH:+:$PYTHONPATH}
PYDEVD_USE_CYTHON=NO
PYDEVD_USE_FRAME_EVAL=NO
PYDEVD_DISABLE_FILE_VALIDATION=1
export PYTHONPATH PYDEVD_USE_CYTHON PYDEVD_USE_FRAME_EVAL PYDEVD_DISABLE_FILE_VALIDATION
lock=${AIMAN_DEBUGPY_LOCK:-/tmp/aiman-debugpy}
if [ -d "$lock" ]; then
  holder=$(cat "$lock/pid" 2>/dev/null)
  if [ -z "$holder" ] || kill -0 "$holder" 2>/dev/null; then
    exec "$py" "$@"
  fi
  rm -rf "$lock"
fi
if ! mkdir "$lock" 2>/dev/null; then
  exec "$py" "$@"
fi
echo $$ >"$lock/pid"
echo "aiman: $py waits for the debugger on $AIMAN_INSPECT" >&2
exec "$py" -m debugpy --listen "$AIMAN_INSPECT" --wait-for-client "$@"
