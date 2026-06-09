"""corndogs Python client: generated client + types (``.gen``) plus a CBOR-over-HTTP transport.

    from corndogs_client import connect, SubmitTaskRequest
    client = connect("https://corndogs.example.com")
    resp = client.submit_task(SubmitTaskRequest(queue="q", priority=0, ...))
"""
from .gen.types import *  # noqa: F401,F403  (generated dataclasses)
from .gen.client import *  # noqa: F401,F403  (CorndogsClient, ServiceError, Transport)
from .transport import CborHttpTransport, connect  # noqa: F401
