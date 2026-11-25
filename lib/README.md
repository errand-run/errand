# errand-lib

The Python side of [errand](https://github.com/errand-run/errand): `import errand`.

```bash
pip install errand-lib
```

A run errand starts needs none of it: errand carries its own copy, of its own version. It is there for a file of work run by
hand (`python test_x.py`), for an editor's completion, and for a package that says things to errand when it runs under it
(`errand.step`, `errand.view`). Outside errand those calls do nothing. No dependencies; the program itself is
`pip install errand-run`.
