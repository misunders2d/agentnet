"""Executable entry point for the AgentNet CLI."""

from __future__ import annotations

from agentnet.cli.parser import build_parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    return int(args.func(args))
