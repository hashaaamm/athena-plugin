"""Infrastructure components, one per concern.

Each component is a `ComponentResource` taking the frozen `StackConfig` and the handful of outputs
it genuinely depends on, so `stack.py` reads as a dependency graph rather than a script. Nothing
here reads `pulumi.Config()` or looks anything up itself — that is what makes a component
constructible in a test.
"""

from components.cluster import Cluster
{%- if cookiecutter.use_postgres == "yes" %}
from components.database import Database
{%- endif %}
from components.identities import DeployTargets, Identities
from components.logs import Logs
{%- if cookiecutter.use_postgres == "yes" %}
from components.migrations import Migrations
{%- endif %}
from components.network import Network
from components.registry import Registry
from components.secrets import Secrets
from components.service import Service

__all__ = [
    "Cluster",
{%- if cookiecutter.use_postgres == "yes" %}
    "Database",
{%- endif %}
    "DeployTargets",
    "Identities",
    "Logs",
{%- if cookiecutter.use_postgres == "yes" %}
    "Migrations",
{%- endif %}
    "Network",
    "Registry",
    "Secrets",
    "Service",
]
