#!/usr/bin/env python3
"""Exercise the native CPA plugin ABI without contacting an upstream service."""

import argparse
import base64
import ctypes
import json
from pathlib import Path


class Buffer(ctypes.Structure):
    _fields_ = [("ptr", ctypes.c_void_p), ("length", ctypes.c_size_t)]


CALL = ctypes.CFUNCTYPE(
    ctypes.c_int, ctypes.c_char_p, ctypes.c_void_p, ctypes.c_size_t,
    ctypes.POINTER(Buffer),
)
FREE = ctypes.CFUNCTYPE(None, ctypes.c_void_p, ctypes.c_size_t)
SHUTDOWN = ctypes.CFUNCTYPE(None)


class PluginAPI(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32), ("call", CALL),
        ("free_buffer", FREE), ("shutdown", SHUTDOWN),
    ]


class HostAPI(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32), ("host_ctx", ctypes.c_void_p),
        ("call", ctypes.c_void_p), ("free_buffer", ctypes.c_void_p),
    ]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("library", type=Path)
    parser.add_argument("--expected-version")
    args = parser.parse_args()
    library = ctypes.CDLL(str(args.library.resolve()))
    library.cliproxy_plugin_init.argtypes = [ctypes.c_void_p, ctypes.POINTER(PluginAPI)]
    library.cliproxy_plugin_init.restype = ctypes.c_int
    api = PluginAPI()
    wrong_host = HostAPI(99, None, None, None)
    assert library.cliproxy_plugin_init(ctypes.byref(wrong_host), ctypes.byref(api)) != 0
    assert library.cliproxy_plugin_init(None, None) != 0
    assert library.cliproxy_plugin_init(None, ctypes.byref(api)) == 0
    assert api.abi_version == 1

    def invoke(method, payload, ok=True):
        raw = json.dumps(payload).encode()
        request = ctypes.create_string_buffer(raw)
        output = Buffer()
        code = api.call(method.encode(), request, len(raw), ctypes.byref(output))
        try:
            response = json.loads(ctypes.string_at(output.ptr, output.length))
        finally:
            api.free_buffer(output.ptr, output.length)
        assert code == 0 and response["ok"] is ok, response
        return response.get("result", {})

    registration = invoke("plugin.register", {"schema_version": 10})
    assert registration["metadata"]["Name"] == "cpa-plugin-gemini"
    assert registration["schema_version"] == 6
    assert registration["capabilities"] == {"request_normalizer": True, "management_api": True}
    if args.expected_version:
        assert registration["metadata"]["Version"] == args.expected_version
    fields = {field["Name"]: field for field in registration["metadata"]["ConfigFields"]}
    assert fields["mode"]["Default"] == "auto"
    assert fields["keep_max_chars"]["Default"] == 2000
    assert fields["keep_max_chars"]["Type"] == "integer"

    resources = invoke("management.register", {})["Resources"]
    assert [resource["Path"] for resource in resources] == ["/ui", "/status"]
    assert resources[0]["Menu"]

    def resource(path, method="GET"):
        result = invoke("management.handle", {"Method": method, "Path": path})
        assert result["Headers"]["Cache-Control"] == ["no-store"]
        return result, base64.b64decode(result["Body"])

    page, html = resource("/ui")
    assert page["StatusCode"] == 200 and "System 搬移监控" in html.decode()
    assert b"data-monitor" in html
    initial = json.loads(resource("/status")[1])
    assert (initial["processed"], initial["relocated"], initial["skipped"]) == (0, 0, 0)
    assert initial["config"]["keep_max_chars"] == 2000
    assert "trigger_words" not in initial["config"]

    def configure(config):
        return invoke("plugin.reconfigure", {
            "schema_version": 10,
            "config_yaml": base64.b64encode(config.encode()).decode(),
        })

    configure("mode: always\n")
    body = b'{"seed":9007199254740993,"requestType":"agent","request":{"systemInstruction":{"parts":[{"text":"Keep answers concise."}]},"contents":[{"role":"user","parts":[{"text":"Hello"}]}]}}'

    def normalize(target="antigravity", request_body=body, source="openai", stream=False):
        return invoke("request.normalize", {
            "FromFormat": source, "ToFormat": target, "Model": "gemini-example",
            "Stream": stream, "Body": base64.b64encode(request_body).decode(),
        })

    for source in ("openai", "openai-response", "claude", "gemini", "interactions"):
        for stream in (True, False):
            result = normalize(source=source, stream=stream)
            transformed = base64.b64decode(result["Body"])
            parsed = json.loads(transformed)
            assert "systemInstruction" not in parsed["request"]
            assert parsed["request"]["contents"][0]["parts"][0]["text"] == "Keep answers concise."
            assert parsed["seed"] == 9007199254740993
            assert parsed["requestType"] == "agent"
            assert normalize(request_body=transformed) == {}, "normalization repeated"
    for target in ("gemini", "vertex", "aistudio", "claude", "codex", "openai", ""):
        assert normalize(target=target) == {}, target
    assert normalize(request_body=b"not JSON") == {}
    assert normalize(request_body=b'{"request":{"systemInstruction":{"parts":[{"inlineData":{"data":"AQID"}}]}}}') == {}
    no_user = b'{"request":{"systemInstruction":{"parts":[{"text":"Rule"}]},"contents":[{"role":"model","parts":[{"text":"History"}]}]}}'
    moved = json.loads(base64.b64decode(normalize(request_body=no_user)["Body"]))
    assert moved["request"]["contents"][0]["parts"][0]["text"] == "Rule"
    assert moved["request"]["contents"][1]["parts"][0]["text"] == "History"

    invoke("plugin.reconfigure", {"schema_version": 10, "config_yaml": base64.b64encode(b"mode: unknown").decode()}, ok=False)
    assert normalize().get("Body"), "failed reconfiguration replaced active configuration"
    configure("mode: off\n")
    assert normalize() == {}
    configure("mode: always\nmodels: claude-*\n")
    assert normalize() == {}
    configure("mode: always\n")
    invoke("plugin.quiesce", {})
    assert normalize() == {}
    configure("mode: always\n")
    invoke("unknown.method", {}, ok=False)

    # Invalid buffers are rejected before copying caller-owned memory.
    for method, pointer, length in ((None, None, 0), (b"request.normalize", None, 1),
                                     (b"request.normalize", None, (64 << 20) + 1)):
        output = Buffer()
        assert api.call(method, pointer, length, ctypes.byref(output)) == 0
        try:
            assert json.loads(ctypes.string_at(output.ptr, output.length))["ok"] is False
        finally:
            api.free_buffer(output.ptr, output.length)
    assert api.call(b"plugin.register", None, 0, None) != 0
    api.free_buffer(None, 0)
    api.shutdown()
    assert normalize() == {}
    final = json.loads(resource("/v0/resource/plugins/cpa-plugin-gemini/status")[1])
    assert (final["processed"], final["relocated"], final["skipped"]) == (28, 12, 16), final
    assert final["quiescing"] and final["config"]["mode"] == "always"
    assert final["skip_reasons"]["quiescing"] == 2
    assert final["version"] == registration["metadata"]["Version"]
    assert not any(secret in json.dumps(final) for secret in ("Keep answers concise.", "Hello", "History"))
    assert resource("/status", "POST")[0]["StatusCode"] == 405
    assert resource("/missing")[0]["StatusCode"] == 404
    print("Native ABI smoke passed: registration, provider isolation, relocation, precision, lifecycle, invalid buffers, monitor resources, and counters.")


if __name__ == "__main__":
    main()
