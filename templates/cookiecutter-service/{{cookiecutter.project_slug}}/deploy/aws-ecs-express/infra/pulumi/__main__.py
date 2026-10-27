"""Entry point. The program is `stack.deploy`; this file only loads its configuration and exports.

Sorted, so `pulumi stack output` lists the pipeline's configuration in the same order every time.
"""

from __future__ import annotations

import pulumi

import config as stack_config
import stack

exports = stack.deploy(stack_config.load())
for name in sorted(exports):
    pulumi.export(name, exports[name])
