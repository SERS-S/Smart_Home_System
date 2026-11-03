#include <boost/asio.hpp>
#include <boost/beast/core.hpp>
#include <boost/beast/websocket.hpp>

#include <vosk_api.h>

#include <iostream>
#include <memory>
#include <stdexcept>
#include <string>
#include <utility>

// #define

namespace net = boost::asio;
namespace beast = boost::beast;
namespace websocket = beast::websocket;
using tcp = net::ip::tcp;

using Recognizer = std::unique_ptr<VoskRecognizer, decltype(&vosk_recognizer_free)>;

void receive_audio(tcp::socket socket, VoskModel* model)
{
    websocket::stream<tcp::socket> ws(std::move(socket));

    // Ограничение размера одного WebSocket-сообщения.
    ws.read_message_max(3200);
    ws.accept();

    Recognizer recognizer(
        vosk_recognizer_new(model, 16000.0f),
        &vosk_recognizer_free
    );

    if (!recognizer) {
        throw std::runtime_error("Cannot create recognizer");
    }

    beast::flat_buffer buffer;

    for (;;) {
        buffer.consume(buffer.size());

        beast::error_code ec;
        ws.read(buffer, ec);

        if (ec == websocket::error::closed) {
            break;
        }

        if (ec) {
            throw beast::system_error(ec);
        }

        // Сохраняет все байты, включая нулевые.
        const std::string data = beast::buffers_to_string(buffer.data());

        if (ws.got_text()) {
            if (data == "stop") {
                break;
            }
            throw std::runtime_error("Unexpected text message");
        }

        // Последний фрагмент может быть короче 3200 байт.
        // Каждый 16-битный отсчёт должен быть передан целиком.
        if (data.empty() || data.size() % 2 != 0) {
            throw std::runtime_error("Invalid PCM fragment");
        }

        const int status = vosk_recognizer_accept_waveform(
            recognizer.get(),
            data.data(),
            static_cast<int>(data.size())
        );

        if (status < 0) {
            throw std::runtime_error("Recognition failed");
        }

        if (status == 1) {
            // Завершённая фраза. Здесь можно публиковать MQTT.
            const std::string result =
                vosk_recognizer_result(recognizer.get());

            std::cout << "Result: " << result << '\n';
        } else {
            // Гипотеза: при поступлении аудио она может измениться.
            const std::string partial =
                vosk_recognizer_partial_result(recognizer.get());

            std::cout << "Partial: " << partial << '\n';
        }
    }

    // Получаем остаток фразы, даже если после неё не было паузы.
    const std::string tail =
        vosk_recognizer_final_result(recognizer.get());

    std::cout << "Final: " << tail << '\n';

    if (ws.is_open()) {
        ws.close(websocket::close_code::normal);
    }
}

int main(int argc, char** argv)
{
    if (argc != 2)
    {
        std::cerr << "Usage: shs_cloud_voice <model-directory>\n";
        return 1;
    }

    try
    {
        // Модель загружается один раз на весь процесс.
        std::unique_ptr<VoskModel, decltype(&vosk_model_free)> model(
            vosk_model_new(argv[1]),
            &vosk_model_free
        );

        if (!model) {
            throw std::runtime_error("Cannot load model");
        }

        net::io_context io;
        tcp::acceptor acceptor(io, tcp::endpoint(tcp::v4(), 9002));

        for (;;) {
            tcp::socket socket(io);
            acceptor.accept(socket);

            try {
                receive_audio(std::move(socket), model.get());
            } catch (const std::exception& e) {
                std::cerr << "Voice session: " << e.what() << '\n';
            }
        }
    }
    catch (const std::exception& e)
    {
        std::cerr << e.what() << '\n';
        return 1;
    }
}
