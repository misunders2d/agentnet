from __future__ import annotations

import pytest
from hypothesis import given, settings, strategies as st

from agentnet.delivery.state import ALLOWED_TRANSITIONS, TERMINAL_FACTS, require_transition
from agentnet.errors import ConflictError
from agentnet.protocol.models import DeliveryFact


FACTS = tuple(DeliveryFact)


@settings(max_examples=1_000, deadline=None)
@given(current=st.sampled_from(FACTS), proposed=st.sampled_from(FACTS))
def test_transition_acceptance_is_exactly_the_declared_graph(current: DeliveryFact, proposed: DeliveryFact) -> None:
    allowed = current == proposed or (current not in TERMINAL_FACTS and proposed in ALLOWED_TRANSITIONS.get(current, frozenset()))
    if allowed:
        require_transition(current, proposed)
    else:
        with pytest.raises(ConflictError):
            require_transition(current, proposed)


@settings(max_examples=250, deadline=None)
@given(current=st.sampled_from(tuple(TERMINAL_FACTS)), proposed=st.sampled_from(FACTS))
def test_terminal_facts_are_absorbing_except_idempotent_restatement(current: DeliveryFact, proposed: DeliveryFact) -> None:
    if current == proposed:
        require_transition(current, proposed)
    else:
        with pytest.raises(ConflictError):
            require_transition(current, proposed)
