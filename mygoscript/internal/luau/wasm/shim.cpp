// Host shim for the Luau VM compiled to WASI. The Go side talks to it through
// a handful of exports and never sees a lua_State.

#include "lua.h"
#include "lualib.h"
#include "luacode.h"

#include <stdlib.h>
#include <string.h>

#define EXPORT(name) extern "C" __attribute__((export_name(name)))

static lua_State* L;
static int hostRef = LUA_NOREF;
static size_t heapUsed;
static size_t heapLimit = 32u << 20;
static long fuel;
static char* result;
static size_t resultLen;

static void* limitedAlloc(void*, void* ptr, size_t osize, size_t nsize)
{
    if (nsize == 0)
    {
        free(ptr);
        heapUsed -= osize;
        return nullptr;
    }
    if (nsize > osize && heapUsed + (nsize - osize) > heapLimit)
        return nullptr;
    void* p = realloc(ptr, nsize);
    if (p)
        heapUsed = heapUsed - osize + nsize;
    return p;
}

// Safepoints fire on loop back edges and calls, so a budget of them bounds
// how long one host call can keep the UI thread. Once spent it stays spent,
// so a script that catches the error with pcall fails at its next safepoint.
static void interrupt(lua_State* T, int gc)
{
    if (gc < 0 && --fuel < 0)
        luaL_errorL(T, "script exceeded its step budget");
}

static void setResult(const char* s, size_t n)
{
    free(result);
    result = (char*)malloc(n + 1);
    memcpy(result, s, n);
    result[n] = 0;
    resultLen = n;
}

EXPORT("mg_alloc") void* mgAlloc(size_t n)
{
    return malloc(n);
}

EXPORT("mg_free") void mgFree(void* p)
{
    free(p);
}

EXPORT("mg_result") const char* mgResult()
{
    return result;
}

EXPORT("mg_result_len") size_t mgResultLen()
{
    return resultLen;
}

static const luaL_Reg libs[] = {
    {"", luaopen_base},
    {LUA_TABLIBNAME, luaopen_table},
    {LUA_STRLIBNAME, luaopen_string},
    {LUA_MATHLIBNAME, luaopen_math},
    {LUA_UTF8LIBNAME, luaopen_utf8},
    {LUA_BITLIBNAME, luaopen_bit32},
    {LUA_BUFFERLIBNAME, luaopen_buffer},
    {nullptr, nullptr},
};

static int load(lua_State* T, const char* name, const char* src, size_t len)
{
    lua_CompileOptions opts = {};
    opts.optimizationLevel = 1;
    size_t size = 0;
    char* bytecode = luau_compile(src, len, &opts, &size);
    int status = luau_load(T, name, bytecode, size, 0);
    free(bytecode);
    return status;
}

static int finish(lua_State* T, int status)
{
    size_t n = 0;
    const char* s = lua_tolstring(T, -1, &n);
    if (status == LUA_OK && !s)
    {
        s = "host function did not return a string";
        n = strlen(s);
        status = LUA_ERRRUN;
    }
    if (!s)
    {
        s = status == LUA_ERRMEM ? "script ran out of memory" : "script raised a non-string error";
        n = strlen(s);
    }
    setResult(s, n);
    return status;
}

// mg_boot runs the trusted prelude with the real globals, keeps the table it
// returns as the host API, then freezes every global table. The VM has only
// the libraries above; WASI gives it no preopened files and no sockets.
EXPORT("mg_boot") int mgBoot(const char* src, size_t len, size_t memLimit, long budget)
{
    heapLimit = memLimit;
    L = lua_newstate(limitedAlloc, nullptr);
    if (!L)
        return -1;
    for (const luaL_Reg* lib = libs; lib->func; lib++)
    {
        lua_pushcfunction(L, lib->func, nullptr);
        lua_pushstring(L, lib->name);
        lua_call(L, 1, 0);
    }
    static const char* const removed[] = {"getfenv", "setfenv", "gcinfo", "newproxy", "collectgarbage", "loadstring"};
    for (const char* name : removed)
    {
        lua_pushnil(L);
        lua_setglobal(L, name);
    }

    fuel = budget;
    int status = load(L, "=mygoscript", src, len);
    if (status == LUA_OK)
        status = lua_pcall(L, 0, 1, 0);
    if (status != LUA_OK || !lua_istable(L, -1))
        return finish(L, status == LUA_OK ? LUA_ERRRUN : status);
    hostRef = lua_ref(L, -1);
    lua_pop(L, 1);

    luaL_sandbox(L);
    lua_callbacks(L)->interrupt = interrupt;
    return LUA_OK;
}

// mg_call compiles src in a fresh sandboxed thread and passes the resulting
// chunk function to host[name]. The prelude decides what the chunk means: a
// plugin script for "mount", a data literal for "render" and "act".
EXPORT("mg_call") int mgCall(const char* name, size_t nameLen, const char* chunk, const char* src, size_t len, long budget)
{
    lua_State* T = lua_newthread(L);
    luaL_sandboxthread(T);
    fuel = budget;

    lua_getref(T, hostRef);
    lua_pushlstring(T, name, nameLen);
    lua_rawget(T, -2);
    lua_remove(T, -2);

    int status = load(T, chunk, src, len);
    if (status == LUA_OK)
        status = lua_pcall(T, 1, 1, 0);
    status = finish(T, status);
    lua_pop(L, 1);
    lua_gc(L, LUA_GCCOLLECT, 0);
    return status;
}
