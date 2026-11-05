// The only code that touches a lua_State. Every Lua call runs under
// lua_pcall here, so no Luau error (a C++ exception) reaches Go frames.

#include "shim.h"

#include "lua.h"
#include "lualib.h"
#include "luacode.h"

#include <chrono>
#include <stdlib.h>
#include <string.h>

using Clock = std::chrono::steady_clock;

struct mg_vm
{
    lua_State* L = nullptr;
    int host = LUA_NOREF;
    mg_limits limits = {};
    size_t heapUsed = 0;
    long fuel = 0;
    Clock::time_point deadline;
};

static void* limitedAlloc(void* ud, void* ptr, size_t osize, size_t nsize)
{
    mg_vm* vm = (mg_vm*)ud;
    if (nsize == 0)
    {
        free(ptr);
        vm->heapUsed -= osize;
        return nullptr;
    }
    if (nsize > osize && vm->heapUsed + (nsize - osize) > vm->limits.heap)
        return nullptr;
    void* p = realloc(ptr, nsize);
    if (p)
        vm->heapUsed = vm->heapUsed - osize + nsize;
    return p;
}

// Safepoints fire on loop back edges and calls. A spent budget stays spent,
// so a script that catches the error with pcall fails at its next safepoint.
// The clock is read every 1024 safepoints to keep the check cheap.
static void interrupt(lua_State* L, int gc)
{
    if (gc >= 0)
        return;
    mg_vm* vm = (mg_vm*)lua_callbacks(L)->userdata;
    if (--vm->fuel < 0)
        luaL_errorL(L, "script exceeded its step budget");
    if ((vm->fuel & 1023) == 0 && Clock::now() > vm->deadline)
    {
        vm->fuel = -1;
        luaL_errorL(L, "script exceeded its time budget");
    }
}

static void arm(mg_vm* vm)
{
    vm->fuel = vm->limits.steps;
    vm->deadline = Clock::now() + std::chrono::microseconds(vm->limits.micros);
}

static char* copy(const char* s, size_t n)
{
    char* out = (char*)malloc(n + 1);
    memcpy(out, s, n);
    out[n] = 0;
    return out;
}

// The compiler allocates outside the Luau heap and has no interrupt, so the
// source size is what bounds its time and memory.
static const size_t maxSource = 256 << 10;

static int load(lua_State* L, const char* name, const char* src, size_t len)
{
    if (len > maxSource)
    {
        lua_pushliteral(L, "script is larger than 256 KiB");
        return LUA_ERRSYNTAX;
    }
    lua_CompileOptions opts = {};
    opts.optimizationLevel = 1;
    size_t size = 0;
    char* bytecode = luau_compile(src, len, &opts, &size);
    int status = luau_load(L, name, bytecode, size, 0);
    free(bytecode);
    return status;
}

// finish turns the value on top of L into *out: the result when status is
// LUA_OK and it is a string, otherwise an error message.
static int finish(lua_State* L, int status, char** out, size_t* outlen)
{
    size_t n = 0;
    const char* s = lua_tolstring(L, -1, &n);
    if (status == LUA_OK && !s)
    {
        s = "host function did not return a string";
        status = LUA_ERRRUN;
        n = strlen(s);
    }
    if (!s)
    {
        s = status == LUA_ERRMEM ? "script ran out of memory" : "script raised a non-string error";
        n = strlen(s);
    }
    *out = copy(s, n);
    if (outlen)
        *outlen = n;
    return status;
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

static const char* const removed[] = {"getfenv", "setfenv", "gcinfo", "newproxy", "collectgarbage", "loadstring", "print"};

static int openlibs(lua_State* L)
{
    for (const luaL_Reg* lib = libs; lib->func; lib++)
    {
        lua_pushcfunction(L, lib->func, nullptr);
        lua_pushstring(L, lib->name);
        lua_call(L, 1, 0);
    }
    for (const char* name : removed)
    {
        lua_pushnil(L);
        lua_setglobal(L, name);
    }
    return 0;
}

static mg_vm* newVM(mg_vm* vm, const char* prelude, size_t len, mg_limits limits, char** err)
{
    vm->limits = limits;
    vm->L = lua_newstate(limitedAlloc, vm);
    if (!vm->L)
    {
        mg_close(vm);
        *err = copy("cannot create a Luau state", 26);
        return nullptr;
    }
    lua_State* L = vm->L;
    arm(vm);

    lua_pushcfunction(L, openlibs, "openlibs");
    int status = lua_pcall(L, 0, 0, 0);
    if (status == LUA_OK)
        status = load(L, "=mygoscript", prelude, len);
    if (status == LUA_OK)
        status = lua_pcall(L, 0, 1, 0);
    if (status == LUA_OK && !lua_istable(L, -1))
    {
        lua_pushliteral(L, "the prelude did not return a table");
        status = LUA_ERRRUN;
    }
    if (status != LUA_OK)
    {
        finish(L, status, err, nullptr);
        mg_close(vm);
        return nullptr;
    }
    vm->host = lua_ref(L, -1);
    lua_pop(L, 1);

    luaL_sandbox(L);
    lua_callbacks(L)->userdata = vm;
    lua_callbacks(L)->interrupt = interrupt;
    return vm;
}

static int call(mg_vm* vm, const char* fn, const char* chunkname, const char* src, size_t len, char** out, size_t* outlen)
{
    lua_State* L = vm->L;
    if (!lua_checkstack(L, 2))
        return finish(L, LUA_ERRMEM, out, outlen);
    lua_State* T = lua_newthread(L);
    luaL_sandboxthread(T);
    arm(vm);

    lua_getref(T, vm->host);
    lua_getfield(T, -1, fn);
    lua_remove(T, -2);

    int status = load(T, chunkname, src, len);
    if (status == LUA_OK)
        status = lua_pcall(T, 1, 1, 0);
    status = finish(T, status, out, outlen);
    lua_pop(L, 1);
    lua_gc(L, LUA_GCCOLLECT, 0);
    return status;
}

// The entry points catch what escapes a pcall (only an allocation failure
// outside one can): an exception must not unwind into Go. The state may be
// half-updated after one, so the caller closes it.
extern "C" mg_vm* mg_new(const char* prelude, size_t len, mg_limits limits, char** err)
{
    mg_vm* vm = new mg_vm;
    try
    {
        return newVM(vm, prelude, len, limits, err);
    }
    catch (...)
    {
        mg_close(vm);
        *err = copy("luau: internal error while starting", 35);
        return nullptr;
    }
}

extern "C" int mg_call(mg_vm* vm, const char* fn, const char* chunkname, const char* src, size_t len, char** out, size_t* outlen)
{
    try
    {
        return call(vm, fn, chunkname, src, len, out, outlen);
    }
    catch (...)
    {
        const char* msg = "luau: internal error";
        *out = copy(msg, strlen(msg));
        *outlen = strlen(msg);
        return MG_BROKEN;
    }
}

extern "C" void mg_close(mg_vm* vm)
{
    if (vm->L)
        lua_close(vm->L);
    delete vm;
}

extern "C" void mg_free_string(char* s)
{
    free(s);
}
