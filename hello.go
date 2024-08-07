package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	// Получение аргументов командной строки
	arg := "запущена без аргументов"
	if len(os.Args) > 1 {
		arg = os.Args[1]
	}

	// Сообщение для вывода в консоль
	message := fmt.Sprintf("Служба Modbus2: %s", arg)

	// Команда для запуска новой консоли и вывода сообщений в нее
	cmd := exec.Command("xterm", "-hold", "-title", "Modbus2", "-e", "bash", "-c", fmt.Sprintf(`
    echo '%s';
    while true; do
      echo 'Служба Modbus2 работает...';
      sleep 10;
    done
  `, message))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Запуск команды
	if err := cmd.Start(); err != nil {
		fmt.Printf("Ошибка при запуске консоли: %v\n", err)
		return
	}

	// Ожидание завершения команды
	if err := cmd.Wait(); err != nil {
		fmt.Printf("Ошибка при выполнении команды: %v\n", err)
	}

	// Сообщение о завершении службы
	fmt.Println("Служба Modbus2 завершена.")
}
