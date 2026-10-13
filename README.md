# Парсер сайтов (Go)

[![hexlet-check](https://github.com/P-Lisaveta/go-from-scratch-project-316/actions/workflows/hexlet-check.yml/badge.svg)](https://github.com/P-Lisaveta/go-from-scratch-project-316/actions)

Разработайте утилиту для обхода и анализа сайтов

Учебный проект Хекслета: https://ru.hexlet.io/programs/go-from-scratch


## Стек

- Go

## Установка

```bash
git clone https://github.com/P-Lisaveta/go-from-scratch-project-316.git
cd go-from-scratch-project-316
make build
```

## Использование

Запустите анализ сайта через `make`:

```bash
make run URL=https://example.com
```

Или запустите CLI напрямую:

```bash
go run ./cmd/hexlet-go-crawler --depth 2 --workers 8 https://example.com
go run ./cmd/hexlet-go-crawler --help
```

Команда `make test` запускает все автоматические тесты. Результат работы — JSON-отчёт с URL корневой страницы, временем формирования и статусом первого HTTP-запроса.

---

<details>
<summary>Автоматические тесты Хекслета</summary>

Тесты запускаются на каждый коммит. За запуск отвечает файл `.github/workflows/hexlet-check.yml` — не удаляйте и не переименовывайте ни его, ни репозиторий.

</details>

## О Хекслете

[Хекслет](https://ru.hexlet.io/) — школа программирования: авторские программы обучения с практикой, поддержкой наставников и реальными проектами, которые остаются в резюме. Этот репозиторий — один из таких проектов.
