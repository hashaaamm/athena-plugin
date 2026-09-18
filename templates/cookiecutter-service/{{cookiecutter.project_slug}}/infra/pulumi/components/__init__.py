"""Infrastructure components, one per concern.

Each component is a `ComponentResource` taking the frozen `StackConfig` and the handful of outputs
it genuinely depends on, so `__main__.py` reads as a dependency graph rather than a script. Nothing
here reads `pulumi.Config()` itself — that is what makes a component constructible in a test.
"""

from components.apis import Apis
{%- if cookiecutter.use_postgres == "yes" %}
from components.database import Database
{%- endif %}
from components.identities import Identities
from components.registry import Registry
from components.secrets import Secrets
from components.service import JobSpec, Service

__all__ = [
    "Apis",
{%- if cookiecutter.use_postgres == "yes" %}
    "Database",
{%- endif %}
    "Identities",
    "JobSpec",
    "Registry",
    "Secrets",
    "Service",
]
