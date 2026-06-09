"""CBOR-over-HTTP transport for the generated corndogs client (./gen).

The wire is CBOR in the POST body to ``{base_url}/v1alpha1/{service}/{method}``;
map/field keys are the CSIL field names verbatim (csilgen docs/cbor-wire-contract.md).

The generated ``Transport.call(service, method, req)`` does not receive the
expected response type (unlike Go's out-param / Rust's ``Res`` generic), so this
transport recovers it by introspecting ``CorndogsClient``'s return annotations
and rebuilds the typed dataclass. (Tracked upstream as a csilgen request; once the
generator passes the type, this introspection can go away.)
"""
from __future__ import annotations

import dataclasses
import typing
import urllib.error
import urllib.request

import cbor2

from .gen.client import CorndogsClient, ServiceError


def _to_plain(value):
    """Convert a request dataclass into CBOR-encodable plain data."""
    if dataclasses.is_dataclass(value) and not isinstance(value, type):
        return {f.name: _to_plain(getattr(value, f.name)) for f in dataclasses.fields(value)}
    if isinstance(value, dict):
        return {k: _to_plain(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [_to_plain(v) for v in value]
    return value


def _from_data(tp, data):
    """Rebuild a typed value of annotation ``tp`` from CBOR-decoded ``data``."""
    if tp is None or tp is typing.Any or data is None:
        return data
    origin = typing.get_origin(tp)
    args = typing.get_args(tp)
    if origin is typing.Union:  # Optional[X] / Union[...]
        non_none = [a for a in args if a is not type(None)]
        return _from_data(non_none[0], data) if len(non_none) == 1 else data
    if origin in (list, typing.List):
        return [_from_data(args[0], v) for v in data]
    if origin in (dict, typing.Dict):
        return {k: _from_data(args[1], v) for k, v in data.items()}
    if dataclasses.is_dataclass(tp):
        hints = typing.get_type_hints(tp)
        kwargs = {f.name: _from_data(hints.get(f.name), data.get(f.name))
                  for f in dataclasses.fields(tp) if f.name in data}
        return tp(**kwargs)
    return data


def _response_types():
    """Map normalized wire-method name -> the client method's return type."""
    out = {}
    hints_cache = {}
    for name, fn in vars(CorndogsClient).items():
        if name.startswith("_") or not callable(fn):
            continue
        ret = hints_cache.get(name)
        if ret is None:
            ret = typing.get_type_hints(fn).get("return")
        if ret is not None:
            out[name.replace("_", "").lower()] = ret
    return out


_RESP_TYPES = _response_types()


class CborHttpTransport:
    def __init__(self, base_url: str, headers: dict | None = None, timeout: float = 30.0):
        self.base_url = base_url.rstrip("/")
        self.headers = headers or {}
        self.timeout = timeout

    def call(self, service: str, method: str, req):
        body = cbor2.dumps(_to_plain(req))
        url = f"{self.base_url}/v1alpha1/{service}/{method}"
        headers = {"content-type": "application/cbor", "accept": "application/cbor", **self.headers}
        http_req = urllib.request.Request(url, data=body, headers=headers, method="POST")
        try:
            with urllib.request.urlopen(http_req, timeout=self.timeout) as resp:
                raw = resp.read()
        except urllib.error.HTTPError as exc:
            raw = exc.read()
            try:
                err = cbor2.loads(raw)
            except Exception:
                raise ServiceError(exc.code, f"http {exc.code}") from None
            raise ServiceError(int(err.get("code", exc.code)), err.get("message", f"http {exc.code}")) from None

        data = cbor2.loads(raw) if raw else None
        return _from_data(_RESP_TYPES.get(method.replace("_", "").lower()), data)


def connect(base_url: str, **kwargs) -> CorndogsClient:
    """Convenience: a CorndogsClient wired to a CborHttpTransport at base_url."""
    return CorndogsClient(CborHttpTransport(base_url, **kwargs))
