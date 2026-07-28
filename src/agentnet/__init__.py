"""AgentNet.

The package exposes a local conformance kernel.  It deliberately labels SQLite
acceptance as ``accepted_local`` and refuses production-only capabilities until
their component and owner gates are present.
"""

from .core.app import CommunicationCore
from .operations.config import ExtensionConfig, RuntimeProfile

__all__ = ["CommunicationCore", "ExtensionConfig", "RuntimeProfile"]
__version__ = "0.1.30"

