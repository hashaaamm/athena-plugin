"""Cross-cutting security primitives: hashing, signing, and the caller a token resolves to.

`core/` and not a layer, because none of this knows a session, a repository or a resource. The
`core-is-independent` contract in `.importlinter` is what keeps it that way, and it is also why
`get_current_actor` can live here: it builds the caller out of the token's claims and loads
nothing. Reading the user row per request instead would move that dependency up into `app/api/`
and cost a round trip on every authenticated call.

Nothing in this package is reversible. There is no "recover the password" path because there is no
encryption anywhere in it: the database holds an Argon2 digest, and a leaked backup yields no
credential.
"""
