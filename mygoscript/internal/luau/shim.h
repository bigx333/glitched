#pragma once

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct mg_vm mg_vm;

typedef struct mg_limits
{
    size_t heap;      // bytes the Luau allocator may hand out
    long steps;       // interrupt safepoints one call may pass
    long long micros; // wall-clock time one call may take
} mg_limits;

// mg_new runs prelude with the standard globals, keeps the table it returns
// as the host API and freezes the globals. On failure it returns NULL and
// sets *err to a message the caller frees with mg_free_string.
mg_vm* mg_new(const char* prelude, size_t len, mg_limits limits, char** err);

// MG_BROKEN from mg_call means the VM must be closed, not called again.
#define MG_BROKEN (-1)

// mg_call compiles src in a fresh sandboxed thread and passes the chunk
// function to host[fn]. It returns 0 and the string the host function
// returned, or nonzero and the error message, in *out (freed with
// mg_free_string).
int mg_call(mg_vm* vm, const char* fn, const char* chunkname, const char* src, size_t len, char** out, size_t* outlen);

void mg_close(mg_vm* vm);
void mg_free_string(char* s);

#ifdef __cplusplus
}
#endif
