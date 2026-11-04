#include <boost/asio.hpp>
#include <boost/beast/core.hpp>
#include <boost/beast/websocket.hpp>

#include <vosk_api.h>
#include "mqtt_publisher.h"

#include <cstddef>
#include <iostream>
#include <memory>
#include <stdexcept>
#include <string>
#include <utility>

namespace net = boost::asio;
namespace beast = boost::beast;
namespace websocket = beast::websocket;
using tcp = net::ip::tcp;

using Recognizer = std::unique_ptr<VoskRecognizer, decltype(&vosk_recognizer_free)>;

constexpr std::size_t SAMPLE_RATE = 16000;
// Дополнительная пауза после сигнала Vosk о конце фразы, в миллисекундах.
constexpr std::size_t ENDPOINT_GRACE_MS = 1000;

void receive_audio(tcp::socket socket, VoskModel* model, voice::MqttPublisher& publisher)
{
    websocket::stream<tcp::socket> ws(std::move(socket));

    // Ограничение размера одного WebSocket-сообщения.
    ws.read_message_max(3200);
    ws.accept();

    Recognizer recognizer(
        vosk_recognizer_new(model, static_cast<float>(SAMPLE_RATE)),
        &vosk_recognizer_free
    );

    if (!recognizer) {
        throw std::runtime_error("Cannot create recognizer");
    }

    beast::flat_buffer buffer;
    std::size_t endpoint_samples = 0;

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

        // Считаем длительность поступившего PCM, а не время между пакетами.
        // Пока Result не вызван, Vosk продолжает распознавать ту же фразу.
        endpoint_samples = status == 1 ? endpoint_samples + data.size() / 2 : 0;

        if (status == 1 && endpoint_samples * 1000 >= SAMPLE_RATE * ENDPOINT_GRACE_MS) {
            // Публикуем только завершённую фразу.
            const std::string result = vosk_recognizer_result(recognizer.get());
            endpoint_samples = 0;

            std::cout << "Result: " << result << '\n';
            publisher.publish_result(result);
        } else {
            // Гипотеза: при поступлении аудио она может измениться.
            const std::string partial = vosk_recognizer_partial_result(recognizer.get());

            std::cout << "Partial: " << partial << '\n';
        }
    }

    // Получаем остаток фразы, даже если после неё не было паузы.
    const std::string tail = vosk_recognizer_final_result(recognizer.get());

    std::cout << "Final: " << tail << '\n';
    publisher.publish_result(tail);

    if (ws.is_open())
    {
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
        const std::string listener_address = REMOTE_VOICE_HOST;
        const unsigned short listener_port = REMOTE_VOICE_PORT;

        std::unique_ptr<VoskModel, decltype(&vosk_model_free)> model
        (
            vosk_model_new(argv[1]),
            &vosk_model_free
        );

        if (!model)
        {
            throw std::runtime_error("Cannot load model");
        }

        voice::MqttPublisher publisher;
        net::io_context io;
        tcp::resolver resolver(io);
        const auto endpoints = resolver.resolve(listener_address, std::to_string(listener_port), tcp::resolver::passive);
        auto endpoint = endpoints.begin()->endpoint();

        for (const auto& candidate : endpoints)
        {
            if (candidate.endpoint().address().is_v4())
            {
                endpoint = candidate.endpoint();
                break;
            }
        }
        tcp::acceptor acceptor(io, endpoint);
        std::cout << "Voice WebSocket listening on " << acceptor.local_endpoint() << std::endl;

        for (;;)
        {
            tcp::socket socket(io);
            acceptor.accept(socket);

            try
            {
                receive_audio(std::move(socket), model.get(), publisher);
            }
            catch (const std::exception& e) {
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
