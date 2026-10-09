.PHONY: fix lint test
fix:
	uv run ruff check --fix .
lint:
	uv run ruff check .
test:
	cd .. && uv run --project eval python -m unittest discover -s eval -t . -p 'test_*.py'
