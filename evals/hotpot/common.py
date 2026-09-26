"""Shared API client for the HotpotQA evaluation harness.

Auth: bridge an admin smoke user into the eval tenant (10011, the fork's
review tenant with live models) using the platform key held server-side by
portal-sim. No secret is ever printed.
"""

from __future__ import annotations

import json
import os
import re
import time

import requests

BASE = os.environ.get("WEKNORA_BASE", "http://localhost:8080")
TENANT_ID = int(os.environ.get("EVAL_TENANT_ID", "10011"))
EVAL_USER = os.environ.get("EVAL_USER", "hotpot-eval-runner")
PLATFORM_KEY_FILE = os.environ.get(
    "PLATFORM_KEY_FILE", "/home/ubuntu/WeKnora-portal-sim/.env"
)
# Tenant 10011 live models (review tenant): embedding / QA / rerank
EMBEDDING_MODEL_ID = "05b7cd01-aec9-4c73-8209-f47bed002579"
SUMMARY_MODEL_ID = "09f242e2-2512-490d-9779-e921b5cdca5a"


def _platform_key() -> str:
    with open(PLATFORM_KEY_FILE, encoding="utf-8") as fh:
        for line in fh:
            if line.startswith("WEKNORA_PLATFORM_KEY="):
                return line.split("=", 1)[1].strip().strip('"')
    raise RuntimeError("WEKNORA_PLATFORM_KEY not found")


class EvalClient:
    def __init__(self) -> None:
        self.http = requests.Session()
        self.token = self._bridge()

    def _headers(self) -> dict:
        return {
            "Authorization": f"Bearer {self.token}",
            "X-Tenant-ID": str(TENANT_ID),
        }

    def _bridge(self) -> str:
        resp = self.http.post(
            f"{BASE}/api/v1/identity/bridge",
            headers={"X-API-Key": _platform_key(), "X-Tenant-ID": str(TENANT_ID)},
            json={
                "uum_user_id": EVAL_USER,
                "tenant_id": TENANT_ID,
                "role": "admin",
                "username": EVAL_USER,
            },
            timeout=30,
        )
        resp.raise_for_status()
        data = resp.json()["data"]
        return data["token"]

    def disable(self) -> None:
        self.http.post(
            f"{BASE}/api/v1/identity/disable",
            headers={"X-API-Key": _platform_key(), "X-Tenant-ID": str(TENANT_ID)},
            json={"uum_user_id": EVAL_USER, "tenant_id": TENANT_ID},
            timeout=30,
        )

    # ── knowledge bases ────────────────────────────────────────────────

    def create_kb(self, name: str) -> str:
        resp = self.http.post(
            f"{BASE}/api/v1/knowledge-bases",
            headers=self._headers(),
            json={
                "name": name,
                "description": "hotpot eval question corpus (10 paragraphs)",
                "embedding_model_id": EMBEDDING_MODEL_ID,
                "summary_model_id": SUMMARY_MODEL_ID,
            },
            timeout=60,
        )
        resp.raise_for_status()
        return resp.json()["data"]["id"]

    def add_manual_knowledge(self, kb_id: str, title: str, content: str) -> str:
        resp = self.http.post(
            f"{BASE}/api/v1/knowledge-bases/{kb_id}/knowledge/manual",
            headers=self._headers(),
            json={"title": title, "content": content, "status": "publish"},
            timeout=60,
        )
        resp.raise_for_status()
        return resp.json()["data"]["id"]

    def list_knowledge(self, kb_id: str) -> list:
        return self.list_knowledge_page(kb_id, 1)

    def list_knowledge_page(self, kb_id: str, page: int, size: int = 100) -> list:
        resp = self.http.get(
            f"{BASE}/api/v1/knowledge-bases/{kb_id}/knowledge",
            headers=self._headers(),
            params={"page": page, "page_size": size},
            timeout=60,
        )
        resp.raise_for_status()
        data = resp.json().get("data")
        if isinstance(data, dict):
            return data.get("list") or data.get("knowledges") or []
        return data or []

    def knowledge_status_summary(self, kb_id: str) -> dict:
        items = self.list_knowledge(kb_id)
        counts: dict[str, int] = {}
        for it in items:
            counts[it.get("status", "?")] = counts.get(it.get("status", "?"), 0) + 1
        return counts

    def delete_kb(self, kb_id: str) -> None:
        self.http.delete(
            f"{BASE}/api/v1/knowledge-bases/{kb_id}", headers=self._headers(), timeout=60
        )

    # ── chat / SSE ─────────────────────────────────────────────────────

    def create_session(self, title: str) -> str:
        resp = self.http.post(
            f"{BASE}/api/v1/sessions",
            headers=self._headers(),
            json={"title": title},
            timeout=30,
        )
        resp.raise_for_status()
        return resp.json()["data"]["id"]

    def ask_sse(
        self,
        session_id: str,
        kb_id: str,
        question: str,
        timeout: int = 180,
        extra_body: dict | None = None,
    ) -> dict:
        """Run one SSE knowledge-chat round; return {answer, references, events}.

        extra_body is merged into the request body last, so experiments can
        add request-level feature flags (e.g. enable_weighted_ranking/ranking).
        """
        body = {
            "query": question,
            "knowledge_base_ids": [kb_id],
            "disable_title": True,
        }
        if extra_body:
            body.update(extra_body)
        events: list[dict] = []
        with self.http.post(
            f"{BASE}/api/v1/knowledge-chat/{session_id}",
            headers={**self._headers(), "Accept": "text/event-stream"},
            json=body,
            stream=True,
            timeout=timeout,
        ) as resp:
            resp.raise_for_status()
            buf = []
            for raw in resp.iter_lines(decode_unicode=True):
                if raw is None:
                    continue
                if raw.startswith("data:"):
                    buf.append(raw[5:].strip())
                elif raw == "" and buf:
                    payload = "\n".join(buf)
                    buf = []
                    try:
                        events.append(json.loads(payload))
                    except json.JSONDecodeError:
                        events.append({"_raw": payload[:200]})
            if buf:
                try:
                    events.append(json.loads("\n".join(buf)))
                except json.JSONDecodeError:
                    pass
        answer = ""
        references = []
        done = False
        for ev in events:
            rtype = ev.get("response_type")
            if rtype == "answer":
                answer += ev.get("content") or ""
            elif rtype == "references":
                references = ev.get("data") or ev.get("content") or []
            elif rtype == "complete":
                done = True
        return {"answer": answer.strip(), "references": references, "done": done}


    def ask_sse_routing(self, session_id: str, question: str, timeout: int = 300) -> dict:
        """Routing-mode SSE round: no KB hint, enable_kb_routing=true.
        Returns ask_sse fields plus routed_kbs (from the kb_router event)."""
        events: list[dict] = []
        with self.http.post(
            f"{BASE}/api/v1/knowledge-chat/{session_id}",
            headers={**self._headers(), "Accept": "text/event-stream"},
            json={"query": question, "enable_kb_routing": True, "disable_title": True},
            stream=True,
            timeout=timeout,
        ) as resp:
            resp.raise_for_status()
            buf = []
            for raw in resp.iter_lines(decode_unicode=True):
                if raw is None:
                    continue
                if raw.startswith("data:"):
                    buf.append(raw[5:].strip())
                elif raw == "" and buf:
                    payload = "\n".join(buf)
                    buf = []
                    try:
                        events.append(json.loads(payload))
                    except json.JSONDecodeError:
                        events.append({"_raw": payload[:200]})
            if buf:
                try:
                    events.append(json.loads("\n".join(buf)))
                except json.JSONDecodeError:
                    pass
        answer = ""
        references = []
        routed_kbs: list[dict] = []
        done = False
        for ev in events:
            rtype = ev.get("response_type")
            data = ev.get("data") or {}
            if rtype == "answer":
                answer += ev.get("content") or ""
            elif rtype == "references":
                references = ev.get("data") or ev.get("content") or []
            elif rtype == "complete":
                done = True
            elif rtype == "tool_call" and isinstance(data, dict):
                if str(data.get("tool_name")) == "kb_router":
                    routed_kbs = (data.get("arguments") or {}).get("selected") or []
        return {"answer": answer.strip(), "references": references, "done": done, "routed_kbs": routed_kbs}


    def ask_sse_agent(
        self,
        session_id: str,
        agent_id: str,
        question: str,
        timeout: int = 420,
        extra_body: dict | None = None,
    ) -> dict:
        """Run one SSE agent-chat round; return {answer, done, tool_hints, steps}.

        The agent endpoint has no `references` event: citations are inline
        <kb/> tags in the answer and the ranked retrieval lives in the
        persisted agent_steps (see load_agent_ref_titles). extra_body is
        merged last so experiments can pass request-level feature flags
        (e.g. enable_weighted_ranking/ranking).
        """
        body = {"query": question, "agent_id": agent_id, "disable_title": True}
        if extra_body:
            body.update(extra_body)
        events: list[dict] = []
        with self.http.post(
            f"{BASE}/api/v1/agent-chat/{session_id}",
            headers={**self._headers(), "Accept": "text/event-stream"},
            json=body,
            stream=True,
            timeout=timeout,
        ) as resp:
            resp.raise_for_status()
            buf = []
            for raw in resp.iter_lines(decode_unicode=True):
                if raw is None:
                    continue
                if raw.startswith("data:"):
                    buf.append(raw[5:].strip())
                elif raw == "" and buf:
                    payload = "\n".join(buf)
                    buf = []
                    try:
                        events.append(json.loads(payload))
                    except json.JSONDecodeError:
                        events.append({"_raw": payload[:200]})
            if buf:
                try:
                    events.append(json.loads("\n".join(buf)))
                except json.JSONDecodeError:
                    pass
        answer = ""
        done = False
        error = None
        tool_hints: list[str] = []
        for ev in events:
            rtype = ev.get("response_type")
            data = ev.get("data") or {}
            if rtype == "answer":
                answer += ev.get("content") or ""
            elif rtype == "complete":
                done = True
            elif rtype == "error":
                error = str(data.get("error") or data)[:200]
            elif rtype == "tool_call":
                hint = data.get("hint")
                if hint and hint not in tool_hints:
                    tool_hints.append(hint)
        return {
            "answer": answer.strip(),
            "done": done,
            "error": error,
            "tool_hints": tool_hints,
        }

    def load_agent_ref_titles(self, session_id: str, expect_secs: float = 90.0) -> list[str] | None:
        """Fetch the last assistant message and return ordered retrieval titles.

        Walks agent_steps and collects knowledge_search result titles in
        result_index order, deduplicated across calls preserving first-seen
        order. Returns None when the message/steps cannot be loaded (mirrors
        the "refs not captured" convention of interrupted SSE rounds).
        """
        import time as _time

        deadline = _time.time() + expect_secs
        messages = None
        while _time.time() < deadline:
            resp = self.http.get(
                f"{BASE}/api/v1/messages/{session_id}/load",
                headers=self._headers(),
                timeout=30,
            )
            if resp.status_code == 200:
                data = resp.json().get("data")
                messages = data if isinstance(data, list) else (data or {}).get("list") or []
                if messages:
                    break
            _time.sleep(2)
        if not messages:
            return None
        assistant = None
        for m in messages:
            if m.get("role") == "assistant":
                assistant = m
        if assistant is None:
            return None
        titles: list[str] = []
        seen: set[str] = set()
        for step in assistant.get("agent_steps") or []:
            for tc in step.get("tool_calls") or []:
                if tc.get("name") not in ("knowledge_search", "search_knowledge"):
                    continue
                results = ((tc.get("result") or {}).get("data") or {}).get("results") or []
                for r in results:
                    t = r.get("knowledge_title")
                    if t and t not in seen:
                        seen.add(t)
                        titles.append(t)
        return titles


def reference_titles(references) -> list[str]:
    """Extract document titles from the references event payload."""
    titles = []
    if isinstance(references, dict):
        references = references.get("references") or references.get("data") or []
    for ref in references or []:
        if not isinstance(ref, dict):
            continue
        for key in ("knowledge_name", "knowledge_title", "title", "name"):
            val = ref.get(key)
            if val:
                titles.append(val)
                break
    return titles
