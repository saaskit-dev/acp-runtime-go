#!/usr/bin/env python3
"""Independent credential-free ACP peer, validating actual Go wire against the
pinned official stable schema. No Go DTOs or production encoders are imported.
"""
import json
import pathlib
import sys
from jsonschema import Draft202012Validator
ROOT = pathlib.Path(__file__).resolve().parent
SCHEMA = json.loads((ROOT / "schema-v1.23.0/schema.json").read_text())
MODE = sys.argv[1] if len(sys.argv) > 1 else "normal"
def validate(definition, value):
    Draft202012Validator({"$ref": "#/$defs/" + definition, "$defs": SCHEMA["$defs"]}).validate(value)
def emit(value):
    print(json.dumps(value, separators=(",", ":")), flush=True)
def result(id, value):
    emit({"jsonrpc": "2.0", "id": id, "result": value})
def read():
    line = sys.stdin.readline()
    if not line:
        raise EOFError()
    value = json.loads(line)
    assert value["jsonrpc"] == "2.0"
    return value
def reverse(id, method, params, response_definition):
    emit({"jsonrpc": "2.0", "id": id, "method": method, "params": params})
    response = read()
    assert response["id"] == id and "error" not in response, response
    validate(response_definition, response["result"])
    return response["result"]
def fixture(name):
    return json.loads((ROOT / "wire" / (name + ".json")).read_text())
while True:
    try:
        request = read()
    except EOFError:
        break
    method, params, id = request["method"], request.get("params", {}), request.get("id")
    if method == "initialize":
        validate("InitializeRequest", params)
        assert params["protocolVersion"] == 1
        value = {"protocolVersion": 1, "agentCapabilities": {"loadSession": True, "sessionCapabilities": {"resume": {}, "list": {}, "close": {}, "delete": {}}, "auth": {"logout": {}}}}
        if MODE == "auth-first":
            assert params["clientCapabilities"]["auth"]["terminal"] is True
            value["authMethods"] = [fixture("terminal-auth")]
        validate("InitializeResponse", value)
        result(id, value)
    elif method == "authenticate":
        raise AssertionError("terminal authentication must never use authenticate")
    elif method == "session/new":
        validate("NewSessionRequest", params)
        value = {"sessionId": "session-42", "configOptions": [fixture("boolean-option")]}
        validate("NewSessionResponse", value)
        result(id, value)
    elif method in ("session/load", "session/resume"):
        validate("LoadSessionRequest" if method == "session/load" else "ResumeSessionRequest", params)
        assert params["sessionId"] == "session-42"
        result(id, {})
    elif method == "session/set_config_option":
        validate("SetSessionConfigOptionRequest", params)
        assert params["type"] == "boolean" and params["value"] is False
        value = {"configOptions": [fixture("boolean-option")]}
        validate("SetSessionConfigOptionResponse", value)
        result(id, value)
    elif method == "session/prompt":
        validate("PromptRequest", params)
        permission = fixture("permission-request")
        response = reverse("permission-allow", "session/request_permission", permission, "RequestPermissionResponse")
        assert response == fixture("permission-selected")
        permission["toolCall"]["title"] = "Reject operation"
        response = reverse("permission-reject", "session/request_permission", permission, "RequestPermissionResponse")
        assert response == fixture("permission-rejected")
        permission["toolCall"]["title"] = "Cancel operation"
        response = reverse("permission-cancel", "session/request_permission", permission, "RequestPermissionResponse")
        assert response == fixture("permission-cancelled")
        form = fixture("elicitation-form")
        form["requestId"] = id
        response = reverse("form", "elicitation/create", form, "CreateElicitationResponse")
        assert response == fixture("elicitation-accept")
        response = reverse("url", "elicitation/create", fixture("elicitation-url"), "CreateElicitationResponse")
        assert response == {"action": "accept"}
        emit({"jsonrpc": "2.0", "method": "elicitation/complete", "params": fixture("elicitation-complete")})
        emit({"jsonrpc": "2.0", "method": "session/update", "params": fixture("usage-update")})
        result(id, {"stopReason": "end_turn"})
    elif method in ("session/close", "session/delete", "logout"):
        validate({"session/close": "CloseSessionRequest", "session/delete": "DeleteSessionRequest", "logout": "LogoutRequest"}[method], params)
        result(id, {})
    else:
        raise AssertionError("unexpected request " + method)
